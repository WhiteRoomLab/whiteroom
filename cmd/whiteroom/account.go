package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const accountUsage = `Usage:
  whiteroom account reset [flags] LOGIN
        set a new password for the account with the login name LOGIN, which is
        read from the standard input
  whiteroom account delete [flags] ID
        delete the account with the ID, also if its row cannot be read

These commands change the data directory itself, so they fail while a
whiteroom serve admin has it open: stop that first.

Run "whiteroom account reset -h" or "whiteroom account delete -h" for the
flags.
`

const accountResetUsage = `Usage: whiteroom account reset [flags] LOGIN

Set a new password for the account with the login name LOGIN: for an
administrator who cannot log in any more, the last one too, or for an account
that was invited and has no password yet. The account is active afterwards, and
its sessions have ended. An account that is disabled is refused: enable it
first, with the admin API.

The password is read from the standard input only, never from a flag, an
argument or the environment. On a terminal, it is asked for twice, without echo,
and again if the two are not the same or the password is refused, at most 3
times; otherwise it is the first line of the input, which must not be empty and
has at most 8192 bytes. The command prints no password and no token.

The command changes the data directory itself, so it fails while a whiteroom
serve admin has it open: stop that first. If another administrator can log in,
the admin API resets a password without that. If the command fails once it has
removed the old password, the account has no password: run the command again.

A row of the accounts that cannot be read stops the command, which names it:
delete it with whiteroom account delete.

With -config, the item data of the YAML file gives what -data leaves out; the
other items are checked and not used. Without it, no config file is read.

Flags:
`

const accountDeleteUsage = `Usage: whiteroom account delete [flags] ID

Delete the account with the ID, such as one that whiteroom account reset names
for a row that cannot be read. A row that cannot be read is deleted by itself,
without the other rows read, however many there are; any other account is
deleted by the rules of the accounts: the last active administrator cannot be
deleted, and a row that cannot be read is in the way. The sessions of the
account end. No document changes: who created or saved one stays as it was. The
standard input is not read.

The command changes the data directory itself, so it fails while a whiteroom
serve admin has it open: stop that first.

With -config, the item data of the YAML file gives what -data leaves out; the
other items are checked and not used. Without it, no config file is read.

Flags:
`

// runAccount runs whiteroom account with args, those after account.
func runAccount(ctx context.Context, args []string, e env) int {
	if len(args) == 0 {
		printf(e.stderr, "%s", accountUsage)
		return 2
	}
	switch args[0] {
	case "reset":
		return accountReset(ctx, args[1:], e)
	case "delete":
		return accountDelete(ctx, args[1:], e)
	case "help", "-h", "-help", "--help":
		printf(e.stdout, "%s", accountUsage)
		return 0
	}
	printf(e.stderr, "whiteroom account: unknown subcommand %q\nRun \"whiteroom help\" for usage.\n", args[0])
	return 2
}

// accountTarget is what whiteroom account reset and delete are given: the data directory and their one argument.
type accountTarget struct {
	name      string // of the command, such as "whiteroom account reset"
	data, arg string
}

// parseAccountArgs parses the arguments of the command name, which has the usage usage and takes one argument, called
// what in the text of a usage error and argument in the usage. If it returns false, the command is done, with the exit
// code code.
func parseAccountArgs(args []string, e env, name, usage, what, argument string) (t accountTarget, code int, ok bool) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(e.stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", usage)
		flags.PrintDefaults()
	}
	var configFile, dataDir string
	flags.StringVar(&configFile, "config", "", "read the settings from the YAML file `FILE`; the flags override them")
	flags.StringVar(&dataDir, "data", "", "use the data directory `DIR` (default data of the config file)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return t, 0, false
		}
		return t, 2, false
	}
	switch flags.NArg() {
	case 0:
		return t, usageError(flags, "no %s: give %s, after the flags", what, argument), false
	case 1:
	default:
		return t, usageError(flags, "too many arguments: give one %s, after the flags", argument), false
	}
	cfg, err := loadConfig(configFile)
	if err != nil {
		printConfigError(e.stderr, flags.Name(), err)
		return t, 2, false
	}
	data := cmp.Or(dataDir, cfg.data)
	if data == "" {
		return t, usageError(flags, "no data directory: give -data DIR, or data in the config file"), false
	}
	return accountTarget{name: flags.Name(), data: data, arg: flags.Arg(0)}, 0, true
}

// openAccounts opens the data directory data as its writer, with the accounts of its store.
func openAccounts(e env, data string) (*store.Store, *account.Manager, error) {
	s, err := store.Open(data)
	if err != nil {
		return nil, nil, err
	}
	m, err := e.newAccounts(s)
	if err != nil {
		return nil, nil, errors.Join(err, s.Close())
	}
	return s, m, nil
}

// adviseOnLock tells on w, if err is that of a store that another process has open as its writer, to stop the server
// that it is, and what the admin API does instead.
func adviseOnLock(w io.Writer, command string, err error, api string) {
	if strings.HasSuffix(err.Error(), ": another process holds the writer lock") {
		printf(w, "%s: stop whiteroom serve admin on this data directory, and run the command again; if another "+
			"administrator can log in, the admin API does without that: %s\n", command, api)
	}
}

// accountReset runs whiteroom account reset with args, those after reset.
func accountReset(ctx context.Context, args []string, e env) int {
	t, code, ok := parseAccountArgs(args, e, "whiteroom account reset", accountResetUsage, "login name", "LOGIN")
	if !ok {
		return code
	}
	p := errorPrinter{w: e.stderr, command: t.name}
	s, m, err := openAccounts(e, t.data)
	if err != nil {
		p.printError(ctx, err)
		adviseOnLock(e.stderr, t.name, err, "POST /api/accounts/{id}/reset")
		return 1
	}
	a, err := resetAccount(ctx, e, m, t)
	closeErr := s.Close()
	switch {
	case err == nil && closeErr == nil:
		printf(e.stdout, "reset the password of %s (%s)\n", a.Login, a.ID)
		return 0
	case errors.Is(err, errProblemsTold):
		err = closeErr
	default:
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		p.printError(ctx, err)
	}
	return 1
}

// shellQuote returns s as one word of a POSIX shell: as it is if it has only letters, digits, the bytes of "_@%+=:,./-" and
// the path separator of the OS, and else in single quotes, in which each single quote is closed, escaped and opened again.
func shellQuote(s string) string {
	plain := s != ""
	for i := 0; i < len(s) && plain; i++ {
		c := s[i]
		plain = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_@%+=:,./-", c) >= 0 ||
			os.IsPathSeparator(c)
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unreadableRow matches the error of an operation on all the accounts that a row that cannot be read fails.
var unreadableRow = regexp.MustCompile(`^accounts "([^"\\]+)": invalid value$`)

// resetAccount does what whiteroom account reset does with the accounts m of the data directory: it finds the account
// with the login name that t has, asks for a password for it, and resets it. It returns the account as it was found.
// Every failure is an error that is printed, but errProblemsTold, which has been told.
func resetAccount(ctx context.Context, e env, m *account.Manager, t accountTarget) (account.Account, error) {
	if _, err := m.List(ctx); err != nil {
		if match := unreadableRow.FindStringSubmatch(err.Error()); match != nil && content.CheckID(content.RefUser, match[1]) == nil {
			err = errors.Join(err, fmt.Errorf("delete it with: whiteroom account delete -data %s %s", shellQuote(t.data), match[1]))
		}
		return account.Account{}, err
	}
	a, err := m.Find(ctx, t.arg)
	if err != nil {
		return account.Account{}, err
	}
	if a.Status == account.StatusDisabled {
		return account.Account{}, fmt.Errorf("the account %s (%s) is disabled: enable it first, with the admin API", a.Login, a.ID)
	}
	var password string
	err = askPassword(ctx, e, t.name, "New password for "+a.Login, func(p string) error {
		if err := account.CheckPassword(p, a); err != nil {
			return err
		}
		password = p
		return nil
	})
	if err != nil {
		return account.Account{}, err
	}
	if err := resetPassword(ctx, m, a, password); err != nil {
		return account.Account{}, err
	}
	return a, nil
}

// resetter is what resetPassword needs of the accounts: the Manager, but for the tests that make the second call fail.
type resetter interface {
	Reset(context.Context, account.Actor, string) (string, error)
	SetPassword(context.Context, account.Client, string, string) (account.Account, error)
}

// resetPassword makes password the password of the account a, as the system does: it resets the account, which removes
// its password and its sessions, and then uses the token that that gives, which the process does not tell anyone. If the
// second step fails, the account is left without a password, and the error says so.
func resetPassword(ctx context.Context, m resetter, a account.Account, password string) error {
	token, err := m.Reset(ctx, account.System, a.ID)
	if err != nil {
		return err
	}
	if _, err := m.SetPassword(ctx, account.Client{}, token, password); err != nil {
		return errors.Join(err, fmt.Errorf("the account %s has no password now: run the command again", a.Login))
	}
	return nil
}

// accountDelete runs whiteroom account delete with args, those after delete.
func accountDelete(ctx context.Context, args []string, e env) int {
	t, code, ok := parseAccountArgs(args, e, "whiteroom account delete", accountDeleteUsage, "account ID", "ID")
	if !ok {
		return code
	}
	p := errorPrinter{w: e.stderr, command: t.name}
	s, m, err := openAccounts(e, t.data)
	if err != nil {
		p.printError(ctx, err)
		adviseOnLock(e.stderr, t.name, err, "DELETE /api/accounts/{id}, for an account whose row can be read")
		return 1
	}
	// Its login name, if its row can be read, for the line that tells it; the error is that of Delete, which reads it too.
	a, getErr := m.Get(ctx, t.arg)
	err = m.Delete(ctx, account.System, t.arg)
	if err = errors.Join(err, s.Close()); err != nil {
		p.printError(ctx, err)
		return 1
	}
	if getErr != nil {
		printf(e.stdout, "deleted account %s (its row could not be read)\n", t.arg)
	} else {
		printf(e.stdout, "deleted account %s (%s)\n", t.arg, a.Login)
	}
	return 0
}
