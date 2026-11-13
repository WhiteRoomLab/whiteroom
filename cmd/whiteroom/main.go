// Command whiteroom builds a site into static files and serves them.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const usage = `Usage:
  whiteroom init [flags] [SITE]
        make the data directory, with the first administrator, and import the
        site in the directory SITE if given
  whiteroom build [flags] [SITE]
        build the site in the directory SITE (default ".") and publish it
  whiteroom serve public [flags] [OUT]
        serve the published release of the output directory OUT (default "out")

Run "whiteroom init -h", "whiteroom build -h" or "whiteroom serve public -h"
for the flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// A second signal then stops the process at once.
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], env{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, newAccounts: account.New})
	stop()
	os.Exit(code)
}

// env is what a command gets from its process.
type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// newAccounts makes the account Manager of a store: account.New, but in the tests cheaper password hashes.
	newAccounts func(*store.Store) (*account.Manager, error)
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
	case "serve":
		return runServe(ctx, args[1:], e.stdout, e.stderr)
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
