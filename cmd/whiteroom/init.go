package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const initUsage = `Usage: whiteroom init [flags] [SITE]

Initialize the store of the daemon whiteroom-db whose sockets are in the directory
of -db, which is running on a data directory that whiteroom-db init has made: init
writes the first commit of the store, makes the first administrator and, with SITE,
imports the site in the directory SITE. The store must have no account and no
document: init refuses one that has, and changes nothing in it. A store that an
earlier init left without an administrator is used.

SITE is read and checked with a trial import, which stores no document and no
record, before the password is asked for: the problems of the site are the problems
of the trial, and init stops there.

The password of the administrator is read from the standard input only. On a
terminal, it is asked for twice, without echo; otherwise it is the first line of
the input, which must not be empty and has at most 8192 bytes.

If init fails after the administrator is made, which is when the import of SITE
fails, the store has the administrator and no site, unless the daemon stored the
import as the command stopped: it tells how to start over. init removes no file.

With -url, or url in the config file, the site in SITE is checked with URL as the
url of its settings, for the site of another environment, such as
https://staging.example.com. The settings that init stores keep the url that
they have. URL is checked as the url of the settings is.

With -config, the items db, theme and url of the YAML file give what -db, -theme
and -url leave out; theme and url are used only with SITE. The other items are
checked and not used: one file may be shared with whiteroom-db. Without -config,
no config file is read.

Flags:
`

// initFlags are the values of the flags of whiteroom init.
type initFlags struct {
	config, db, login, name, email, theme, url string
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
	flags.StringVar(&f.db, "db", "", "connect to the daemon whose sockets are in the directory `DIR` (default db of the config file)")
	flags.StringVar(&f.login, "login", "", "the login name `LOGIN` of the first administrator (required)")
	flags.StringVar(&f.name, "name", "", "the name `NAME` of the first administrator, to show (required)")
	flags.StringVar(&f.email, "email", "", "the email address `ADDRESS` of the first administrator (required)")
	flags.StringVar(&f.theme, "theme", "", "use the theme in the directory `DIR` instead of SITE/theme, to check SITE")
	flags.StringVar(&f.url, "url", "", "check SITE as if the url of its settings were `URL`; they are stored as they are (default "+
		"url of the config file)")
	return flags, &f
}

// newStoreKinds are the kinds of the documents that a store has none of when whiteroom init may use it, as edit.Import
// requires: the settings and the kinds that it imports.
var newStoreKinds = []content.RefKind{
	store.SettingsKind, content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu,
	content.RefForm, content.RefRedirect,
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
	if f.url != "" && !importing {
		return usageError(flags, "-url is for SITE: give the directory SITE of the site to import")
	}
	cfg, err := loadConfig(f.config)
	if err != nil {
		printConfigError(e.stderr, flags.Name(), err)
		return 2
	}
	db := cmp.Or(f.db, cfg.db)
	if db == "" {
		return usageError(flags, errNoDB)
	}
	site, theme, override := flags.Arg(0), "", ""
	if importing {
		theme = cmp.Or(f.theme, cfg.theme)
		var ok bool
		if override, ok = siteURLOverride(e.stderr, flags.Name(), f.url, cfg); !ok {
			return 2
		}
	}
	p := errorPrinter{w: e.stderr, command: flags.Name(), site: site, theme: theme}

	// Before the store is made, so that an interrupted command makes nothing.
	if err := ctx.Err(); err != nil {
		p.printError(ctx, err)
		return 1
	}
	s, err := openNewStore(db)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	r := initRun{e: e, p: p, db: db, store: s, site: site, theme: theme, url: override,
		in: account.Input{Login: f.login, Name: f.name, Email: f.email, Role: account.RoleAdmin}}
	admin, err := r.do(ctx)
	if err == nil {
		err = s.Close()
	} else {
		_ = s.Close()
	}
	switch {
	case errors.Is(err, errProblemsTold):
		return 1
	case err != nil:
		p.printError(ctx, err)
		if r.stranded {
			printf(e.stderr, "%s: the store of %s now has the administrator %s, and no site, unless the daemon stored the import "+
				"as the command stopped; to start over, stop whiteroom-db, remove its data directory, make it again with "+
				"whiteroom-db init, start it, and run whiteroom init again\n", flags.Name(), db, admin.Login)
		}
		return 1
	}

	printf(e.stdout, "initialized the store of %s\n", db)
	printf(e.stdout, "created the administrator %s (%s)\n", admin.Login, admin.ID)
	if importing {
		printf(e.stdout, "imported the site %s\n", site)
	}
	return 0
}

// openNewStore opens the store of the daemon whose sockets are in the directory db as its writer, and makes it, with its
// first commit, if it has none. It is closed if it is refused.
func openNewStore(db string) (*store.Store, error) {
	s, err := store.Open(dbDialer(db))
	if errors.Is(err, store.ErrNoStore) {
		return store.Create(dbDialer(db))
	}
	return s, err
}

// initRun is what whiteroom init does with its store, in the order that its usage tells.
type initRun struct {
	e          env
	p          errorPrinter
	db         string
	store      *store.Store
	site       string
	theme, url string
	in         account.Input

	stranded bool // the administrator is made and the import of the site has failed
}

// do checks that the store is new, and then, for a site, that it imports, with a trial import that commits nothing; it asks
// for the password, makes the administrator and imports the site. The error is that to print, or errProblemsTold for
// problems that have been printed.
func (r *initRun) do(ctx context.Context) (account.Account, error) {
	if err := r.checkNew(ctx); err != nil {
		return account.Account{}, err
	}
	var src edit.Source
	if r.site != "" {
		opened, closeSite, err := openSite(r.site, r.theme)
		if err != nil {
			return account.Account{}, err
		}
		defer closeSite()
		src = opened
		trialUser, err := content.NewIDSource(rand.Reader).NewID(content.RefUser, time.Now())
		if err != nil {
			return account.Account{}, err
		}
		// Before the password is read, so that nothing is asked for that cannot be used; its warnings are those of the import.
		if _, err := edit.ImportWith(ctx, r.store, src, trialUser, edit.ImportOptions{URL: r.url, Trial: true}); err != nil {
			return account.Account{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return account.Account{}, err
	}

	accounts, err := r.e.newAccounts(r.store)
	if err != nil {
		return account.Account{}, err
	}
	var admin account.Account
	err = askPassword(ctx, r.e, r.p.command, "Password", func(password string) error {
		var err error
		admin, err = accounts.CreateFirst(ctx, r.in, password)
		return err
	})
	if err != nil {
		return account.Account{}, err
	}
	if src == nil {
		return admin, nil
	}
	warnings, err := edit.ImportWith(ctx, r.store, src, admin.ID, edit.ImportOptions{URL: r.url})
	for _, w := range warnings {
		r.p.printProblem(w)
	}
	if err != nil {
		r.stranded = true
	}
	return admin, err
}

// checkNew refuses a store that has an account or a document, before anything is written or asked for.
func (r *initRun) checkNew(ctx context.Context) error {
	return r.store.View(ctx, func(tx *store.Tx) error {
		rows, err := tx.Accounts()
		switch {
		case err != nil:
			return err
		case len(rows) > 0:
			return fmt.Errorf("the store of %s has accounts: init is for a new store", r.db)
		}
		for _, kind := range newStoreKinds {
			docs, err := tx.Documents(kind)
			switch {
			case err != nil:
				return err
			case len(docs) > 0:
				return fmt.Errorf("the store of %s has documents: init is for a new store", r.db)
			}
		}
		return nil
	})
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
