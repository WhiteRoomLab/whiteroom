package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/publish"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const doctorUsage = `Usage: whiteroom doctor [flags] [SITE]

Look for what is wrong in the data directory, and print each problem on a line of
the standard output, which starts with the part that it is in:

  open      the data directory cannot be opened to be read; nothing else is checked
  engine    the log and the objects of the storage engine
  index     the rows of the index, against the documents
  original  the originals of the media that the documents refer to
  audit     the chains of the audit logs, and the heads given with -heads
  heads     a line of the file of -heads that is not a head, or no head in it
  site      what is published, which a build checks first: the published documents
            against the schema, and the paths of the outputs (with SITE)

The last line tells how many problems there are, or that the command stopped after
1000 of them. The exit code is 0 if there are none, 1 if there are some or a check
could not be made (what could not be made is on the standard error), and 2 for a
usage error.

The data directory is opened as a process that reads it: nothing is written in it,
nothing is repaired, and a whiteroom serve admin may run on it. The command does
not look at the originals that only old versions refer to, at drafts, at the
templates of the pages and the images (what a build does after the checks), and at
the accounts and the sessions; to check those, build with -data into other -out
and -cache directories.

With SITE, or site in the config file, the published documents are checked as a
build from the data directory checks them: from SITE it reads only the schema file
site/schema.yaml and the theme. Without SITE, the command says on the standard
error that it has not checked them.

-heads reads the heads of the audit logs that a whiteroom serve admin has written
to its standard error, or whiteroom backup to its standard output, which were kept
outside the data directory: the lines that have "audit head " are used, the others
skipped, so that a log can be given as it is. Each must be the hash of the record
of its number. After a restore, give the heads that whiteroom backup printed, not
those that the server wrote after the copy was taken.

With -config, the items data, site and theme of the YAML file give what -data,
SITE and -theme leave out; the other items are checked and not used. Without it,
no config file is read.

Flags:
`

// maxDoctorProblems is the most problems that whiteroom doctor looks for.
const maxDoctorProblems = 1000

// maxHeadLine is the most bytes of a line of the file of -heads that is read; a line with more is not a head.
const maxHeadLine = 1 << 20

// headMarker is what a line of the heads of the audit logs has before the head.
const headMarker = "audit head "

// runDoctor runs whiteroom doctor with args, those after doctor.
func runDoctor(ctx context.Context, args []string, e env) int {
	return doctor(ctx, args, e, maxDoctorProblems)
}

// doctor runs whiteroom doctor, which stops when it has found limit problems.
func doctor(ctx context.Context, args []string, e env, limit int) int {
	flags := flag.NewFlagSet("whiteroom doctor", flag.ContinueOnError)
	flags.SetOutput(e.stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", doctorUsage)
		flags.PrintDefaults()
	}
	var configFile, dataDir, headsFile, themeDir string
	flags.StringVar(&configFile, "config", "", "read the settings from the YAML file `FILE`; the flags and SITE override them")
	flags.StringVar(&dataDir, "data", "", "check the data directory `DIR` (default data of the config file)")
	flags.StringVar(&headsFile, "heads", "", "compare the audit logs with the heads in the file `FILE`, \"-\" for the standard input")
	flags.StringVar(&themeDir, "theme", "", "use the theme in the directory `DIR` instead of SITE/theme (default theme of the config file)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		return usageError(flags, "too many arguments: give at most one SITE, after the flags")
	}
	cfg, err := loadConfig(configFile)
	if err != nil {
		printConfigError(e.stderr, flags.Name(), err)
		return 2
	}
	data := cmp.Or(dataDir, cfg.data)
	if data == "" {
		return usageError(flags, "no data directory: give -data DIR, or data in the config file")
	}
	// Not ".": a site is checked only when it is asked for.
	site := cmp.Or(flags.Arg(0), cfg.site)
	if themeDir != "" && site == "" {
		return usageError(flags, "-theme is for SITE: give the directory SITE of the site to check, or site in the config file")
	}
	theme := ""
	if site != "" {
		theme = cmp.Or(themeDir, cfg.theme)
	}
	p := errorPrinter{w: e.stderr, command: flags.Name(), site: site, theme: theme, stored: true}
	fail := func(err error) int {
		p.printError(ctx, err)
		return 1
	}

	// What cannot be read here is no problem of the data directory: the command stops before it opens it.
	var heads []audit.Head
	var headProblems []string
	if headsFile != "" {
		if heads, headProblems, err = readHeads(e.stdin, headsFile, limit); err != nil {
			return fail(err)
		}
	}
	var schema content.SiteFile
	var themeFS fs.FS
	if site != "" {
		var closeSite func()
		if schema, themeFS, closeSite, err = openAdminSite(site, theme); err != nil {
			return fail(err)
		}
		defer closeSite()
	}

	rep := &report{w: e.stdout, limit: limit}
	st, err := store.OpenReader(data)
	if err != nil {
		rep.add("open: " + err.Error())
		return rep.finish(data)
	}
	defer func() { _ = st.Close() }()
	if err := st.Check(ctx, func(pr store.Problem) bool { return rep.add(pr.String()) }); err != nil {
		return fail(err)
	}
	if !rep.full() {
		if err := st.VerifyAudit(ctx, heads); err != nil {
			if ctx.Err() != nil {
				return fail(err)
			}
			rep.add("audit: " + err.Error())
		}
	}
	for _, line := range headProblems {
		if !rep.add(line) {
			break
		}
	}
	switch {
	case rep.full():
	case site == "":
		printf(e.stderr, "%s: note: no SITE, so the published documents are not checked against the schema file and the "+
			"theme; give SITE, or site in the config file\n", flags.Name())
	default:
		if err := checkSite(ctx, st, schema, themeFS, p, rep); err != nil {
			return fail(err)
		}
	}
	return rep.finish(data)
}

// checkSite checks what is published in st, as a build from it does before it writes a release, and reports each problem
// to rep. The error is that of a check that cannot be made.
func checkSite(ctx context.Context, st *store.Store, schema content.SiteFile, theme fs.FS, p errorPrinter, rep *report) error {
	src, err := publish.Snapshot(ctx, st, schema, theme)
	if err != nil {
		return err
	}
	// Not left to Check, which returns this error as it is, with those that a failing source has.
	if _, _, err := src.Settings(); errors.Is(err, fs.ErrNotExist) {
		rep.add("site: the data directory has no site settings")
		return nil
	}
	var es content.Errors
	switch err := build.Check(ctx, src); {
	case err == nil:
	case errors.As(err, &es):
		for _, e := range es {
			c := *e
			if c.File != "" {
				c.File = p.path(c.File)
			}
			if !rep.add("site: " + c.Error()) {
				break
			}
		}
	default:
		return err
	}
	return nil
}

// report writes the problems that whiteroom doctor finds on w, a line each, up to limit of them.
type report struct {
	w        io.Writer
	limit, n int
}

var oneLine = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// add writes the problem, unless the report is full, and reports whether the check goes on: it does not when the report
// is full, and so when this is the last problem that it takes.
func (r *report) add(problem string) bool {
	if r.full() {
		return false
	}
	r.n++
	printf(r.w, "%s\n", oneLine.Replace(problem))
	return !r.full()
}

func (r *report) full() bool { return r.n >= r.limit }

// finish writes the last line, which tells how many problems there are in the data directory data, and returns the exit
// code.
func (r *report) finish(data string) int {
	switch {
	case r.full():
		printf(r.w, "stopped after %s in %s\n", count(r.n, "problem"), data)
	case r.n == 0:
		printf(r.w, "no problems found in %s\n", data)
		return 0
	default:
		printf(r.w, "%s found in %s\n", count(r.n, "problem"), data)
	}
	return 1
}

// readHeads reads the heads of the audit logs from the file, "-" for stdin: the lines that have "audit head ", of which
// the last such part is the head, as audit.ParseHead reads it, the others skipped. A line that has that and no head is a
// problem, and so is a file without such a line; the problems, at most limit of them, name the file and the line, and have
// nothing of the line.
func readHeads(stdin io.Reader, file string, limit int) (heads []audit.Head, problems []string, err error) {
	r := stdin
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return nil, nil, err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	br := bufio.NewReader(r)
	found := false
	for n := 1; ; n++ {
		line, err := nextHeadLine(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", file, err)
		}
		if !bytes.Contains(line, []byte(headMarker)) {
			continue
		}
		found = true
		h, err := audit.ParseHead(string(line))
		switch {
		case err == nil:
			heads = append(heads, h)
		case len(problems) < limit:
			problems = append(problems, fmt.Sprintf("heads: %s:%d: %v", file, n, err))
		}
	}
	if !found && len(problems) < limit {
		problems = append(problems, "heads: "+file+": no line with a head of an audit log")
	}
	return heads, problems, nil
}

// nextHeadLine reads the next line of r, without its line break; it is nil if the line has more than maxHeadLine bytes,
// the rest of which is read and dropped. The error is io.EOF after the last line.
func nextHeadLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	long, read := false, false
	for {
		part, more, err := r.ReadLine()
		switch {
		case errors.Is(err, io.EOF) && read:
			return line, nil
		case err != nil:
			return nil, err
		}
		read = true
		if len(line)+len(part) > maxHeadLine {
			line, long = nil, true
		} else if !long {
			line = append(line, part...)
		}
		if !more {
			return line, nil
		}
	}
}
