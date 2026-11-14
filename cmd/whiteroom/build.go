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

With -data, or data in the config file, the documents and the originals of the
media are those of the data directory, which the build opens as a process that
reads it and does not write it: a whiteroom serve admin may serve it at the same
time, if the build has other -out and -cache directories. From SITE it reads only
the schema file site/schema.yaml and the theme. Without -data and data, the whole
site is read from SITE.

With -config, the items data, site, theme, out, cache, workers and memory of the
YAML file give what the flags and SITE leave out. Without it, no config file is
read.

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

// buildFlags are the values of the flags of whiteroom build.
type buildFlags struct {
	config, out, cache, theme string
	workers                   int
	memory                    int64
	dataDir                   string // -data, of whiteroom build only: whiteroom serve admin has a flag of its own
}

// newBuildFlags returns the flags of whiteroom build, which print on stderr, and where they put their values.
func newBuildFlags(stderr io.Writer) (*flag.FlagSet, *buildFlags) {
	flags := flag.NewFlagSet("whiteroom build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", buildUsage)
		flags.PrintDefaults()
	}
	f := addBuildFlags(flags)
	flags.StringVar(&f.dataDir, "data", "", "read the documents and the originals from the data directory `DIR` instead of SITE "+
		"(default data of the config file)")
	return flags, f
}

// addBuildFlags adds to flags the flags that whiteroom build and whiteroom serve admin share, and returns where they put
// their values.
func addBuildFlags(flags *flag.FlagSet) *buildFlags {
	var f buildFlags
	flags.StringVar(&f.config, "config", "", "read the settings from the YAML file `FILE`; the flags and SITE override them")
	flags.StringVar(&f.out, "out", "", "write the releases into the output directory `DIR` (default SITE/out)")
	flags.StringVar(&f.cache, "cache", "", "cache the files of the images in `DIR` (default SITE/cache)")
	flags.StringVar(&f.theme, "theme", "", "use the theme in the directory `DIR` instead of SITE/theme")
	flags.IntVar(&f.workers, "workers", 0, "work on at most `N` pages or files at once (0 means the number of CPUs)")
	flags.Int64Var(&f.memory, "memory", 0, "make the images in at most about `MiB` mebibytes at once (0 means 1024)")
	return &f
}

// rangeError returns what is wrong with the values of -workers and -memory, or "" if they are in range.
func (f *buildFlags) rangeError() string {
	switch {
	case f.workers < 0:
		return fmt.Sprintf("-workers %d is out of range (0 or more)", f.workers)
	case f.memory < 0 || f.memory > maxMemory:
		return fmt.Sprintf("-memory %d is out of range (0 to %d)", f.memory, maxMemory)
	}
	return ""
}

// buildSettings is what whiteroom build works with.
type buildSettings struct {
	site, out, cache string
	theme            string // "" for SITE/theme
	workers          int
	memory           int64 // MiB
}

// settings returns the settings from the flags, parsed into f, and the argument of flags, over those of cfg, over the
// defaults. A number of cfg is used unless the command line gives its flag, with any value.
func (f *buildFlags) settings(flags *flag.FlagSet, cfg config) buildSettings {
	s := buildSettings{site: cmp.Or(flags.Arg(0), cfg.site, "."), theme: cmp.Or(f.theme, cfg.theme)}
	s.out = cmp.Or(f.out, cfg.out, filepath.Join(s.site, "out"))
	s.cache = cmp.Or(f.cache, cfg.cache, filepath.Join(s.site, "cache"))
	s.workers, s.memory = cfg.workers, cfg.memory
	if flagGiven(flags, "workers") {
		s.workers = f.workers
	}
	if flagGiven(flags, "memory") {
		s.memory = f.memory
	}
	return s
}

// runBuild runs whiteroom build with args, those after build.
func runBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, f := newBuildFlags(stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		return usageError(flags, "too many arguments: give at most one SITE, after the flags")
	}
	if msg := f.rangeError(); msg != "" {
		return usageError(flags, "%s", msg)
	}
	cfg, err := loadConfig(f.config)
	if err != nil {
		printConfigError(stderr, flags.Name(), err)
		return 2
	}
	s := f.settings(flags, cfg)
	p := errorPrinter{w: stderr, command: flags.Name(), site: s.site, theme: s.theme}
	if data := cmp.Or(f.dataDir, cfg.data); data != "" {
		return buildFromStore(ctx, stdout, p, data, s)
	}

	src, closeSite, err := openSite(s.site, s.theme)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	defer closeSite()
	return buildAndReport(ctx, stdout, p, src, s)
}

// buildAndReport builds src as s says, and prints the warnings and the error, if any, with p, and the summary on stdout. It
// returns the exit code.
func buildAndReport(ctx context.Context, stdout io.Writer, p errorPrinter, src build.Source, s buildSettings) int {
	res, err := build.Build(ctx, build.Options{
		Source: src, Out: s.out, CacheDir: s.cache, Memory: s.memory << 20, Workers: s.workers,
	})
	if res != nil {
		for _, w := range res.Warnings {
			p.printProblem(w)
		}
		printf(stdout, "%s\n", summary(res, s.out))
	}
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	return 0
}

// openSite opens the site directory site, with the theme in the directory theme unless it is "", and checks that it has
// its settings and its schema, and a theme that opens. closeSite closes what openSite has opened.
func openSite(site, theme string) (src build.Source, closeSite func(), err error) {
	d, err := sitedir.Open(site)
	if err != nil {
		return nil, nil, err
	}
	src, closeSite = d, func() { _ = d.Close() }
	if theme != "" {
		root, err := os.OpenRoot(theme)
		if err != nil {
			closeSite()
			return nil, nil, err
		}
		src, closeSite = themedSource{Dir: d, theme: root.FS()}, func() { _ = root.Close(); _ = d.Close() }
	}
	// Not left to Build, which makes the output and the cache directories before it reads the site.
	if _, _, err := src.Settings(); err != nil {
		closeSite()
		return nil, nil, err
	}
	if _, err := src.Theme(); err != nil {
		closeSite()
		return nil, nil, err
	}
	return src, closeSite, nil
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

// errorPrinter prints on w the problems and the errors of command, such as "whiteroom build", on the site in the
// directory site, with the theme in the directory theme if it is not "". If stored is true, the documents are those of a
// data directory, which have no files in site: only the schema file and the theme are there.
type errorPrinter struct {
	w                    io.Writer
	command, site, theme string
	stored               bool
}

// printProblem prints e, a problem in a file of the site, with the path of the file from the working directory.
func (p errorPrinter) printProblem(e *content.Error) {
	c := *e
	if c.File != "" {
		c.File = p.path(c.File)
	}
	printf(p.w, "%s\n", c.Error())
}

// path returns the path from the working directory of file, a path with "/" in the site directory; for a data directory,
// the name of the document in it, which is not a path of the site directory, is the same.
func (p errorPrinter) path(file string) string {
	if rest, ok := strings.CutPrefix(file, "theme"); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		if p.theme != "" {
			return filepath.Join(p.theme, filepath.FromSlash(rest))
		}
	} else if p.stored && file != "site" && file != "site/schema.yaml" {
		return file
	}
	return filepath.Join(p.site, filepath.FromSlash(file))
}

// printError prints err, an error of the command, a line for each part: each problem of the site as printProblem does,
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
		printf(p.w, "%s: interrupted\n", p.command)
	default:
		printf(p.w, "%s: %v\n", p.command, err)
	}
}
