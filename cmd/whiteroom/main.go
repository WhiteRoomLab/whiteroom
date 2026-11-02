// Command whiteroom builds a site into static files and serves them.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `Usage:
  whiteroom build [flags] [SITE]
        build the site in the directory SITE (default ".") and publish it
  whiteroom serve public [flags] [OUT]
        serve the published release of the output directory OUT (default "out")

Run "whiteroom build -h" or "whiteroom serve public -h" for the flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// A second signal then stops the process at once.
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run runs the command with args, those after its name, and returns its exit code: 0 on success, 1 on failure and 2
// on a usage error.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printf(stderr, "%s", usage)
		return 2
	}
	switch args[0] {
	case "build":
		return runBuild(ctx, args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		printf(stdout, "%s", usage)
		return 0
	}
	printf(stderr, "whiteroom: unknown command %q\nRun \"whiteroom help\" for usage.\n", args[0])
	return 2
}

// printf writes to w, stdout or stderr, where nothing can be done if writing fails.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
