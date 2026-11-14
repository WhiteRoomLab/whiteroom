package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const initUsage = `Usage: whiteroom init [flags] [SITE]

Make the data directory, which must be missing or empty, with the first
administrator, and import the site in the directory SITE if it is given.

The password of the administrator is read from the standard input only. On a
terminal, it is asked for twice, without echo; otherwise it is the first line of
the input, which must not be empty and has at most 8192 bytes.

With -config, the items data and theme of the YAML file give what -data and
-theme leave out; theme is used only with SITE. Without it, no config file is
read.

Flags:
`

// initFlags are the values of the flags of whiteroom init.
type initFlags struct {
	config, data, login, name, email, theme string
}

func newInitFlags(stderr io.Writer) (*flag.FlagSet, *initFlags) {
	flags := flag.NewFlagSet("whiteroom init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", initUsage)
		flags.PrintDefaults()
	}
	var f initFlags
	flags.StringVar(&f.config, "config", "", "read the settings from the YAML file `FILE`; the flags override them")
	flags.StringVar(&f.data, "data", "", "make the data directory `DIR` (default data of the config file)")
	flags.StringVar(&f.login, "login", "", "the login name `LOGIN` of the first administrator (required)")
	flags.StringVar(&f.name, "name", "", "the name `NAME` of the first administrator, to show (required)")
	flags.StringVar(&f.email, "email", "", "the email address `ADDRESS` of the first administrator (required)")
	flags.StringVar(&f.theme, "theme", "", "use the theme in the directory `DIR` instead of SITE/theme, to check SITE")
	return flags, &f
}

// runInit runs whiteroom init with args, those after init.
func runInit(ctx context.Context, args []string, e env) int {
	flags, f := newInitFlags(e.stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		return usageError(flags, "too many arguments: give at most one SITE, after the flags")
	}
	for _, required := range []struct{ flag, value string }{{"login", f.login}, {"name", f.name}, {"email", f.email}} {
		if required.value == "" {
			return usageError(flags, "-%s is required", required.flag)
		}
	}
	importing := flags.NArg() == 1
	if f.theme != "" && !importing {
		return usageError(flags, "-theme is for SITE: give the directory SITE of the site to import")
	}
	cfg, err := loadConfig(f.config)
	if err != nil {
		printConfigError(e.stderr, flags.Name(), err)
		return 2
	}
	data := cmp.Or(f.data, cfg.data)
	if data == "" {
		return usageError(flags, "no data directory: give -data DIR, or data in the config file")
	}
	site, theme := flags.Arg(0), ""
	if importing {
		theme = cmp.Or(f.theme, cfg.theme)
	}
	p := errorPrinter{w: e.stderr, command: flags.Name(), site: site, theme: theme}

	// Before the password is read, so that nothing is asked for that cannot be used.
	missing, err := checkNewDataDir(data)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	var src edit.Source
	if importing {
		opened, closeSite, err := openSite(site, theme)
		if err != nil {
			p.printError(ctx, err)
			return 1
		}
		defer closeSite()
		src = opened
	}
	if err := ctx.Err(); err != nil {
		p.printError(ctx, err)
		return 1
	}

	var s *store.Store
	made := false // whether there may be something of init's to clean up: store.Create has been called
	// fail prints err, if there is one, and undoes what init has made, and returns the exit code of a failure.
	fail := func(err error) int {
		if err != nil {
			p.printError(ctx, err)
		}
		if made {
			if err := cleanupNewDataDir(s, data, missing); err != nil {
				printf(e.stderr, "%s: cleaning up %s: %v\n", flags.Name(), data, err)
			}
		}
		return 1
	}
	in := account.Input{Login: f.login, Name: f.name, Email: f.email, Role: account.RoleAdmin}
	var accounts *account.Manager
	var admin account.Account
	err = askPassword(ctx, e, flags.Name(), "Password", func(password string) error {
		if accounts == nil {
			// Again, as another process may have made the directory meanwhile, whose files are not ours to remove.
			var err error
			if missing, err = checkNewDataDir(data); err != nil {
				return err
			}
			made = true
			if s, err = store.Create(data); err != nil {
				return err
			}
			if accounts, err = e.newAccounts(s); err != nil {
				return err
			}
		}
		var err error
		admin, err = accounts.CreateFirst(ctx, in, password)
		return err
	})
	switch {
	case errors.Is(err, errProblemsTold):
		return fail(nil)
	case err != nil:
		return fail(err)
	}
	if importing {
		warnings, err := edit.Import(ctx, s, src, admin.ID)
		for _, w := range warnings {
			p.printProblem(w)
		}
		if err != nil {
			return fail(err)
		}
	}
	err = s.Close()
	s = nil
	if err != nil {
		return fail(err)
	}

	printf(e.stdout, "created the data directory %s\n", data)
	printf(e.stdout, "created the administrator %s (%s)\n", admin.Login, admin.ID)
	if importing {
		printf(e.stdout, "imported the site %s\n", site)
	}
	return 0
}

// checkNewDataDir checks that dir can be made a data directory, which must be missing or an empty directory, and
// reports whether it is missing.
func checkNewDataDir(dir string) (missing bool, err error) {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, err
	case !info.IsDir():
		return false, fmt.Errorf("%s: not a directory; the data directory must be missing or an empty directory", dir)
	}
	entries, err := os.ReadDir(dir)
	switch {
	case err != nil:
		return false, err
	case len(entries) > 0:
		return false, fmt.Errorf("%s: not empty; the data directory must be missing or an empty directory", dir)
	}
	return false, nil
}

// cleanupNewDataDir closes the store s, unless it is nil, and removes what init has made in the data directory dir:
// logdb/ and files/, and dir itself if it was missing. It leaves the parents that making dir has made.
func cleanupNewDataDir(s *store.Store, dir string, missing bool) error {
	var errs []error
	if s != nil {
		errs = append(errs, s.Close())
	}
	errs = append(errs, os.RemoveAll(filepath.Join(dir, "logdb")), os.RemoveAll(filepath.Join(dir, "files")))
	if missing {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// printInitProblems prints on w the problems that the account operation finds in the flags and in the password, a line for
// each, with the flag or the password that each is about.
func printInitProblems(w io.Writer, command string, problems content.Errors) {
	for _, e := range problems {
		what := ""
		switch e.Path {
		case "":
		case "/login", "/name", "/email":
			what = "-" + strings.TrimPrefix(e.Path, "/") + ": "
		case "/password":
			what = "the password: "
		default:
			what = strings.TrimPrefix(e.Path, "/") + ": "
		}
		printf(w, "%s: %s%s\n", command, what, e.Msg)
	}
}

// onlyPasswordProblems reports whether the problems are all about the password, which can be asked for again.
func onlyPasswordProblems(problems content.Errors) bool {
	for _, e := range problems {
		if e.Path != "/password" {
			return false
		}
	}
	return len(problems) > 0
}
