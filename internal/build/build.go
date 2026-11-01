package build

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/imaging"
)

// Options are the options of Build.
type Options struct {
	Source Source // the site to build
	// Out is the output directory, which Build makes if it is missing. Build writes each release into
	// Out/releases/<name> and points the symbolic link Out/current to the release that it publishes, so that the web root
	// of the site is Out/current/public.
	Out string
	// CacheDir is the cache directory of the files of images (imaging.Options.Dir), which builds that run at the same
	// time cannot share. It must not be Out, be in it, or have it in it.
	CacheDir string
	// Memory is about the most bytes that making the files of images takes at once (imaging.Options.Memory); 0 is 1 GiB.
	Memory int64
	// Workers is the most goroutines that render the pages, publish the media or read the files of the release at once;
	// 0 is runtime.GOMAXPROCS(0).
	Workers int
}

// Result is what Build has published.
type Result struct {
	Release string // the name of the release that Out/current points to: 16 hex digits of a hash of its content
	Changed bool   // whether Out/current points to another release than before
	Pages   int    // the pages written: those of the entries, the pages of the lists, and the 404 pages
	Files   int    // the files in the public directory of the release, the pages included
	// Warnings are the problems that do not stop the build, such as a page template that the theme does not have, in
	// the files of the site, sorted by file, line, column, pointer and message, without duplicates.
	Warnings []*content.Error
}

// Build builds the site of opts.Source into a new release in opts.Out and publishes it. It reads and checks the site:
// the settings, the documents and the theme, what is published, and the paths of the outputs, which must differ. It
// then writes the release: into its public directory, the pages, the site CSS and the page CSS, the assets of the
// theme, the feeds, the sitemap, the media that they use and .htaccess; beside it, server.json and nginx.conf. It
// names the release after its content, and points Out/current to it at once, by renaming a new symbolic link over it.
//
// Out holds only what Build makes: .lock, which keeps two builds from running in it at once; the directory releases,
// with the releases; and current. Build keeps the release that current pointed to before, for a rollback, and removes
// the older ones; it removes none if the release is that of current. It also removes what builds that did not end
// left: releases/.tmp-* and current.tmp-*. Anything else in Out is an error, and Build never removes it.
//
// If Build fails, Out stays as it was: Build removes the temporary directory of the release, and makes only Out, .lock
// and releases if they are missing. The error is then content.Errors, sorted by file, line, column and pointer, if the
// site has problems: documents that do not validate, outputs with the same paths, originals of media that do not match
// their documents; ctx.Err() if ctx is done; or else another error, such as one of the Source or of the file system,
// which says what failed. If Build has published the release but cannot remove the old releases or prune the cache,
// it returns the Result with that error.
//
// The same site gives the same files and the same release name, however many workers there are and whatever the
// cache has. Build needs flock(2), which Linux and other Unix systems have, to lock Out.
func Build(ctx context.Context, opts Options) (*Result, error) {
	switch {
	case opts.Source == nil:
		return nil, errors.New("no source")
	case opts.Out == "":
		return nil, errors.New("no output directory")
	case opts.CacheDir == "":
		return nil, errors.New("no cache directory")
	case opts.Memory < 0:
		return nil, fmt.Errorf("memory %d is out of range (0 or more)", opts.Memory)
	case opts.Workers < 0:
		return nil, fmt.Errorf("workers %d is out of range (0 or more)", opts.Workers)
	}
	workers := cmp.Or(opts.Workers, runtime.GOMAXPROCS(0))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkDirs(opts.Out, opts.CacheDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(opts.Out)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	// Also before the lock, so that no .lock is made in a directory of other files.
	if _, err := checkLayout(root); err != nil {
		return nil, err
	}
	unlock, err := lockOut(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	l, err := checkLayout(root)
	if err != nil {
		return nil, err
	}
	images, err := imaging.New(imaging.Options{Dir: opts.CacheDir, Memory: opts.Memory})
	if err != nil {
		return nil, err
	}

	b, err := load(opts.Source)
	if err != nil {
		return nil, err
	}
	b.res = newResolver(b, images, min(workers, 4))
	var p problems
	b.resolveRedirects(b.res.mediaPath, &p)
	b.checkPaths(&p)
	if p.len() > 0 {
		return nil, p.sorted()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	tmp, rel, err := newRelease(root)
	if err != nil {
		return nil, err
	}
	name, files, err := b.writeRelease(ctx, rel, workers, &p)
	_ = rel.Close()
	if err == nil {
		err = ctx.Err()
	}
	changed := false
	if err == nil {
		changed, err = publish(root, tmp, name, l.current)
	}
	if err != nil {
		if rerr := root.RemoveAll(tmp); rerr != nil {
			err = errors.Join(err, outError(root, rerr))
		}
		return nil, err
	}

	res := &Result{Release: name, Changed: changed, Pages: len(b.pages), Files: files, Warnings: b.warnings.sorted()}
	keep := l.releases
	if changed {
		keep = []string{name, l.current}
	}
	return res, errors.Join(removeOld(root, l, keep), images.Prune())
}

// writeRelease writes the release of b into rel, the root of its directory, in at most workers goroutines, and returns
// its name and the number of the files in its public directory (releaseName). It writes into the public directory the
// pages, the site CSS, the page CSS and the assets (renderPages), the feeds and the sitemap, and the media that they
// use (publishMedia), whose problems go to p; and then, unless p has problems or the outputs have duplicates, which
// it checks again with the files written, the files for the servers (writeServerFiles). The error is p.sorted() if p
// has problems, ctx.Err() if ctx is done, or else that of a stage, after what failed.
func (b *builder) writeRelease(ctx context.Context, rel *os.Root, workers int, p *problems) (string, int, error) {
	pub, err := rel.OpenRoot(publicDir)
	if err != nil {
		return "", 0, outError(rel, err)
	}
	defer func() { _ = pub.Close() }()
	fail := func(what string, err error) (string, int, error) {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, fmt.Errorf("%s: %w", what, err)
	}
	if err := b.renderPages(ctx, pub, workers); err != nil {
		return fail("rendering the pages", err)
	}
	if err := b.writeFeeds(ctx, pub); err != nil {
		return fail("writing the feeds", err)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if err := b.writeSitemap(pub); err != nil {
		return fail("writing the sitemap", err)
	}
	if err := b.publishMedia(ctx, pub, workers, p); err != nil {
		return fail("publishing the media", err)
	}
	if p.len() == 0 {
		b.checkPaths(p)
	}
	if p.len() > 0 {
		return "", 0, p.sorted()
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if err := b.writeServerFiles(rel); err != nil {
		return fail("writing the files for the servers", err)
	}
	return releaseName(ctx, rel, workers)
}

// checkDirs checks that out, the output directory, and cache, the cache directory, are apart: Prune would remove the
// files of the output directory if it were in the cache directory, and the next build would refuse an output directory
// that holds the cache directory.
func checkDirs(out, cache string) error {
	o, err := realPath(out)
	if err != nil {
		return err
	}
	c, err := realPath(cache)
	if err != nil {
		return err
	}
	if within(o, c) || within(c, o) {
		return fmt.Errorf("the output directory %s and the cache directory %s must not be the same, or one in the other",
			out, cache)
	}
	return nil
}

// realPath returns the absolute path of name, with the symbolic links resolved in the part of it that exists.
func realPath(name string) (string, error) {
	p, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest), nil
		}
		dir := filepath.Dir(p)
		if dir == p {
			return filepath.Join(p, rest), nil
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = dir
	}
}

// within reports whether the absolute path a is b or in it.
func within(a, b string) bool {
	rel, err := filepath.Rel(b, a)
	return err == nil && filepath.IsLocal(rel)
}
