// Command whiteroom-db is the DB daemon of whiteroom: it alone opens a data directory, and serves it to the admin server
// and the other commands on two Unix domain sockets.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/configfile"
)

const usage = `Usage:
  whiteroom-db serve [flags]
        run the daemon of the data directory, which no other process opens, on the
        sockets in the directory -db, until interrupted
  whiteroom-db init [flags]
        make the data directory, which must be missing or empty
  whiteroom-db compact [flags]
        write the state of the data directory as a new generation of its log, and
        remove the old one; the daemon must be stopped
  whiteroom-db gc [flags]
        remove the files of the data directory that its state does not need; the
        daemon must be stopped

Run "whiteroom-db serve -h", "whiteroom-db init -h", "whiteroom-db compact -h" or
"whiteroom-db gc -h" for the flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// A second signal then stops the process at once.
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], env{
		stdout: os.Stdout, stderr: os.Stderr,
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
	stdout, stderr io.Writer
	// ticks makes the minutes of the export of the heads: the channel that gets a time every minute, and the function that
	// stops it.
	ticks func() (<-chan time.Time, func())
}

// run runs the command with args, those after its name, and returns its exit code: 0 on success, 1 on failure and 2 on a
// usage error.
func run(ctx context.Context, args []string, e env) int {
	if len(args) == 0 {
		printf(e.stderr, "%s", usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], e)
	case "init":
		return runInit(ctx, args[1:], e)
	case "compact":
		return runCompact(ctx, args[1:], e)
	case "gc":
		return runGC(ctx, args[1:], e)
	case "help", "-h", "-help", "--help":
		printf(e.stdout, "%s", usage)
		return 0
	}
	printf(e.stderr, "whiteroom-db: unknown command %q\nRun \"whiteroom-db help\" for usage.\n", args[0])
	return 2
}

// printf writes to w, stdout or stderr, where nothing can be done if writing fails.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// newFlagSet makes the flags of a command, which print usage and then their defaults on stderr.
func newFlagSet(name, usage string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", usage)
		flags.PrintDefaults()
	}
	return flags
}

// parse parses args, which take no arguments after the flags. It returns false if the command is to end with code: 0 for
// a request for help, else 2.
func parse(flags *flag.FlagSet, args []string) (code int, ok bool) {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	if flags.NArg() > 0 {
		return usageError(flags, "unexpected argument %q: the command takes flags only", flags.Arg(0)), false
	}
	return 0, true
}

// usageError prints the problem with the arguments of the command, and its usage, and returns the exit code 2.
func usageError(flags *flag.FlagSet, format string, args ...any) int {
	printf(flags.Output(), "%s: %s\n", flags.Name(), fmt.Sprintf(format, args...))
	flags.Usage()
	return 2
}

// flagGiven reports whether the command line gave the flag name, also with its default value.
func flagGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}

// loadConfig reads the config file file, if it is not "". If it cannot, it prints why, and returns false: a usage error.
func loadConfig(w io.Writer, command, file string) (configfile.File, bool) {
	cfg, err := configfile.Load(file)
	if err != nil {
		printConfigError(w, command, err)
		return configfile.File{}, false
	}
	return cfg, true
}

// printConfigError prints on w the error err of configfile.Load for command: a line for each problem in the file, and any
// other error after the name of the command.
func printConfigError(w io.Writer, command string, err error) {
	var es configfile.Problems
	if !errors.As(err, &es) {
		printf(w, "%s: %v\n", command, err)
		return
	}
	for _, e := range es {
		printf(w, "%s\n", e.Error())
	}
}

// printConfigProblem prints on w the problem msg with the item at path in the config file.
func printConfigProblem(w io.Writer, file, path, msg string) {
	printf(w, "%s\n", (&configfile.Problem{File: file, Path: path, Msg: msg}).Error())
}
