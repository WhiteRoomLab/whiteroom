//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// daemonCommand is a command that connects to the daemon, by the name that it prints before an error, with the arguments.
type daemonCommand struct {
	name string
	args []string
}

// writerCommands returns the commands that connect to the daemon of the sockets in db as its writer, with the arguments that take
// them as far as the daemon; work is a directory that the one that serves is given for its files.
func writerCommands(db, work string) []daemonCommand {
	return []daemonCommand{
		{"whiteroom init", append([]string{"init"}, adminFlags("-db", db)...)},
		{"whiteroom account reset", []string{"account", "reset", "-db", db, initLogin}},
		{"whiteroom account delete", []string{"account", "delete", "-db", db, "usr_x"}},
		{"whiteroom serve admin", []string{"serve", "admin", "-db", db, "-addr", "127.0.0.1:0", "-theme", exampleTheme,
			"-tmp", filepath.Join(work, "tmp"), "-out", filepath.Join(work, "out"), "-cache", filepath.Join(work, "cache"), sampleSite}},
	}
}

// TestDeniedWriter runs the commands that connect as the writer against a daemon that refuses the Hello of the writer as the
// one of a user that is not its writer, as the daemon does for a process of another user: each prints the sentence of the
// daemon after its name, ends with 1, and asks the daemon once. The daemon of the tests runs in the process of the test, which
// is always its writer's, so that this daemon is a fake one that says what whiteroom-db says.
func TestDeniedWriter(t *testing.T) {
	const text = "the user 4242 is not the writer of this daemon"
	var hellos atomic.Int32
	dir := fakeDaemon(t, func(_ dbwire.Role, rd *dbwire.Reader, wr *dbwire.Writer) {
		if kind, err := rd.Next(); err != nil || kind != dbwire.KindHello {
			return
		}
		_ = rd.Discard()
		hellos.Add(1)
		_ = wr.Fail(dbwire.CodeDenied, text)
	})
	for _, tt := range writerCommands(dir, t.TempDir()) {
		t.Run(tt.name, func(t *testing.T) {
			hellos.Store(0)
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), tt.args, testEnv(failingReader{t}, &stdout, &stderr))
			if want := tt.name + ": " + text + "\n"; code != 1 || stdout.Len() != 0 || stderr.String() != want {
				t.Errorf("%s = %d, with stdout %q and stderr %q, want 1, nothing and %q", tt.name, code, stdout.String(),
					stderr.String(), want)
			}
			if n := hellos.Load(); n != 1 {
				t.Errorf("%s asked the daemon %d times, want once: a refusal of the user is not tried again", tt.name, n)
			}
		})
	}
}

// TestSilentDaemon runs the commands against a daemon that takes the connection, reads the Hello and answers nothing, as a
// daemon that is stopped (SIGSTOP) or blocked does: the socket files are there, and the kernel completes the connect. Each of
// them ends when its context ends, as an interrupt of it does, with 1 and the line that tells that it was interrupted.
func TestSilentDaemon(t *testing.T) {
	hello := make(chan struct{}, 16)
	dir := fakeDaemon(t, func(_ dbwire.Role, rd *dbwire.Reader, _ *dbwire.Writer) {
		if kind, err := rd.Next(); err != nil || kind != dbwire.KindHello {
			return
		}
		_ = rd.Discard()
		select {
		case hello <- struct{}{}:
		default:
		}
		for {
			if _, err := rd.Next(); err != nil {
				return
			}
			_ = rd.Discard()
		}
	})
	work := t.TempDir()
	commands := append(writerCommands(dir, work),
		daemonCommand{"whiteroom doctor", []string{"doctor", "-db", dir}},
		daemonCommand{"whiteroom build", []string{"build", "-db", dir, "-theme", exampleTheme, "-out", filepath.Join(work, "build-out"),
			"-cache", filepath.Join(work, "build-cache"), sampleSite}},
	)
	for _, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			for len(hello) > 0 {
				<-hello
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var stdout, stderr bytes.Buffer
			done := async(func() int { return run(ctx, tt.args, testEnv(failingReader{t}, &stdout, &stderr)) })
			select {
			case <-hello:
			case code := <-done:
				t.Fatalf("%s = %d before the daemon was asked, with stdout %q and stderr %q", tt.name, code, stdout.String(), stderr.String())
			case <-time.After(adminWait):
				t.Fatalf("%s has not connected to the daemon in %v", tt.name, adminWait)
			}
			cancel()
			select {
			case code := <-done:
				if want := tt.name + ": interrupted\n"; code != 1 || stdout.Len() != 0 || stderr.String() != want {
					t.Errorf("%s = %d, with stdout %q and stderr %q, want 1, nothing and %q", tt.name, code, stdout.String(),
						stderr.String(), want)
				}
			case <-time.After(10 * time.Second):
				t.Errorf("%s has not returned in 10 s after its context is done, with a daemon that answers nothing", tt.name)
			}
		})
	}
}
