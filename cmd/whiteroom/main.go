// Command whiteroom builds a site into static files and serves them.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const usage = `Usage:
  whiteroom init [flags] [SITE]
        make the data directory, with the first administrator, and import the
        site in the directory SITE if given
  whiteroom build [flags] [SITE]
        build the site in the directory SITE (default ".") and publish it
  whiteroom build -data DIR [flags] [SITE]
        the same, but from the data directory DIR, of which SITE gives only the
        schema file and the theme
  whiteroom rollback OUT [RELEASE]
        publish a release that the output directory OUT still holds, instead of
        the published one
  whiteroom backup [flags] DEST
        copy the data directory to DEST, while a whiteroom serve admin may write it
  whiteroom doctor [flags] [SITE]
        look for what is wrong in the data directory, and in what it publishes
        if SITE is given, without changing anything
  whiteroom serve public [flags] [OUT]
        serve the published release of the output directory OUT (default "out")
  whiteroom serve admin [flags] [SITE]
        serve the admin API of the data directory, and build the site from it
        into the output directory

Run "whiteroom init -h", "whiteroom build -h", "whiteroom rollback -h",
"whiteroom backup -h", "whiteroom doctor -h", "whiteroom serve public -h" or
"whiteroom serve admin -h" for the flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// A second signal then stops the process at once.
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], env{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, newAccounts: account.New,
		ticks: func() (<-chan time.Time, func()) {
			t := time.NewTicker(time.Minute)
			return t.C, t.Stop
		},
	})
	stop()
	os.Exit(code)
}

// env is what a command gets from its process.
type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// newAccounts makes the account Manager of a store: account.New, but in the tests cheaper password hashes.
	newAccounts func(*store.Store) (*account.Manager, error)
	// ticks makes the minutes of serve admin: the channel that gets a time every minute, and the function that stops it.
	ticks func() (<-chan time.Time, func())
}

// run runs the command with args, those after its name, and returns its exit code: 0 on success, 1 on failure and 2
// on a usage error.
func run(ctx context.Context, args []string, e env) int {
	if len(args) == 0 {
		printf(e.stderr, "%s", usage)
		return 2
	}
	switch args[0] {
	case "init":
		return runInit(ctx, args[1:], e)
	case "build":
		return runBuild(ctx, args[1:], e.stdout, e.stderr)
	case "rollback":
		return runRollback(ctx, args[1:], e)
	case "backup":
		return runBackup(ctx, args[1:], e)
	case "doctor":
		return runDoctor(ctx, args[1:], e)
	case "serve":
		return runServe(ctx, args[1:], e)
	case "help", "-h", "-help", "--help":
		printf(e.stdout, "%s", usage)
		return 0
	}
	printf(e.stderr, "whiteroom: unknown command %q\nRun \"whiteroom help\" for usage.\n", args[0])
	return 2
}

// printf writes to w, stdout or stderr, where nothing can be done if writing fails.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
