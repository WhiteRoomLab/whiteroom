package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

const buildUsage = `Usage: whiteroom build [flags] [SITE]

Build the site in the directory SITE (default ".") into a new release in the
output directory, and publish it.

Flags:
`

// maxMemory is the most MiB of -memory, whose bytes must fit in an int64.
const maxMemory int64 = math.MaxInt64 >> 20

// themedSource is a site directory with the theme of another directory.
type themedSource struct {
	*sitedir.Dir
	theme fs.FS
}

func (s themedSource) Theme() (fs.FS, error) { return s.theme, nil }

// runBuild runs whiteroom build with args, those after build.
func runBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("whiteroom build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", buildUsage)
		flags.PrintDefaults()
	}
	var out, cache, theme string
	var workers int
	var memory int64
	flags.StringVar(&out, "out", "", "write the releases into the output directory `DIR` (default SITE/out)")
	flags.StringVar(&cache, "cache", "", "cache the files of the images in `DIR` (default SITE/cache)")
	flags.StringVar(&theme, "theme", "", "use the theme in the directory `DIR` instead of SITE/theme")
	flags.IntVar(&workers, "workers", 0, "work on at most `N` pages or files at once (0 means the number of CPUs)")
	flags.Int64Var(&memory, "memory", 0, "make the images in at most about `MiB` mebibytes at once (0 means 1024)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch {
	case flags.NArg() > 1:
		return usageError(flags, "too many arguments: give at most one SITE, after the flags")
	case workers < 0:
		return usageError(flags, "-workers %d is out of range (0 or more)", workers)
	case memory < 0 || memory > maxMemory:
		return usageError(flags, "-memory %d is out of range (0 to %d)", memory, maxMemory)
	}
	site := cmp.Or(flags.Arg(0), ".")
	out = cmp.Or(out, filepath.Join(site, "out"))
	cache = cmp.Or(cache, filepath.Join(site, "cache"))
	p := errorPrinter{w: stderr, site: site, theme: theme}

	d, err := sitedir.Open(site)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	defer func() { _ = d.Close() }()
	var src build.Source = d
	if theme != "" {
		root, err := os.OpenRoot(theme)
		if err != nil {
			p.printError(ctx, err)
			return 1
		}
		defer func() { _ = root.Close() }()
		src = themedSource{Dir: d, theme: root.FS()}
	}
	// Not left to Build, which makes the output and the cache directories before it reads the site.
	if _, _, err := src.Settings(); err != nil {
		p.printError(ctx, err)
		return 1
	}
	if _, err := src.Theme(); err != nil {
		p.printError(ctx, err)
		return 1
	}

	res, err := build.Build(ctx, build.Options{
		Source: src, Out: out, CacheDir: cache, Memory: memory << 20, Workers: workers,
	})
	if res != nil {
		for _, w := range res.Warnings {
			p.printProblem(w)
		}
		printf(stdout, "%s\n", summary(res, out))
	}
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	return 0
}

// usageError prints the message of a usage error and the usage of flags, as flags.Parse does for the errors that it
// finds, and returns the exit code of usage errors.
func usageError(flags *flag.FlagSet, format string, args ...any) int {
	printf(flags.Output(), "%s: %s\n", flags.Name(), fmt.Sprintf(format, args...))
	flags.Usage()
	return 2
}

// summary returns the line that tells what Build has published into out, as res says.
func summary(res *build.Result, out string) string {
	s := fmt.Sprintf("published release %s in %s", res.Release, out)
	if !res.Changed {
		s = fmt.Sprintf("release %s in %s is already published", res.Release, out)
	}
	s += ": " + count(res.Pages, "page") + ", " + count(res.Files, "file")
	if n := len(res.Warnings); n > 0 {
		s += ", " + count(n, "warning")
	}
	return s
}

// count returns n and noun, in the plural unless n is 1.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// errorPrinter prints on w the problems and the errors of a build of the site in the directory site, with the theme
// in the directory theme if it is not "".
type errorPrinter struct {
	w           io.Writer
	site, theme string
}

// printProblem prints e, a problem in a file of the site, with the path of the file from the working directory.
func (p errorPrinter) printProblem(e *content.Error) {
	c := *e
	if c.File != "" {
		c.File = p.path(c.File)
	}
	printf(p.w, "%s\n", c.Error())
}

// path returns the path from the working directory of file, a path with "/" in the site directory.
func (p errorPrinter) path(file string) string {
	if p.theme != "" {
		if rest, ok := strings.CutPrefix(file, "theme"); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
			return filepath.Join(p.theme, filepath.FromSlash(rest))
		}
	}
	return filepath.Join(p.site, filepath.FromSlash(file))
}

// printError prints err, an error of the build, a line for each part: each problem of the site as printProblem does,
// "interrupted" for ctx.Err(), and any other error after the name of the command.
func (p errorPrinter) printError(ctx context.Context, err error) {
	// Before errors.As, which would find the problems in one of the errors and leave out the others.
	if join, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range join.Unwrap() {
			p.printError(ctx, e)
		}
		return
	}
	var es content.Errors
	var e *content.Error
	switch {
	case errors.As(err, &es):
		for _, e := range es {
			p.printProblem(e)
		}
	case errors.As(err, &e):
		p.printProblem(e)
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		printf(p.w, "whiteroom build: interrupted\n")
	default:
		printf(p.w, "whiteroom build: %v\n", err)
	}
}
