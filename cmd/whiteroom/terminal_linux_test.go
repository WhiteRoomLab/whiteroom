package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// pty is a pseudo terminal: a command reads and writes its slave, as it does a terminal, and the test types at its master
// and reads there what the terminal shows.
type pty struct {
	t             *testing.T
	master, slave *os.File

	mu    sync.Mutex
	shown bytes.Buffer
}

// openPTY opens a pseudo terminal, which a test of the terminal needs, and skips the test if there is none. The master
// has no read deadline, so a goroutine reads it until it is closed.
func openPTY(t *testing.T) *pty {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo terminal: %v", err)
	}
	var unlock int32
	var number uint32
	if err := errors.Join(
		ioctl(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)),
		ioctl(master, syscall.TIOCGPTN, unsafe.Pointer(&number)),
	); err != nil {
		_ = master.Close()
		t.Skipf("no pseudo terminal: %v", err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(number), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		t.Skipf("no pseudo terminal: %v", err)
	}
	p := &pty{t: t, master: master, slave: slave}
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			p.mu.Lock()
			p.shown.Write(buf[:n])
			p.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return p
}

// show returns what the terminal has shown so far, with the line breaks as the text has them.
func (p *pty) show() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.ReplaceAll(p.shown.String(), "\r\n", "\n")
}

// typeText types text, as a user does, where a carriage return is the Enter key.
func (p *pty) typeText(text string) {
	p.t.Helper()
	if _, err := p.master.Write([]byte(text)); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pty) termios() syscall.Termios {
	p.t.Helper()
	var tm syscall.Termios
	if err := ioctl(p.slave, syscall.TCGETS, unsafe.Pointer(&tm)); err != nil {
		p.t.Fatal(err)
	}
	return tm
}

// waitFor waits until cond is true, and stops the test if it is not in 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited 5 s for %s", what)
		}
	}
}

// waitNoEcho waits until the terminal does not echo what is typed.
func (p *pty) waitNoEcho() {
	p.t.Helper()
	waitFor(p.t, "the terminal to stop echoing", func() bool { return p.termios().Lflag&syscall.ECHO == 0 })
}

// ptyInit is a whiteroom init, or another command that asks for a password, that runs with a pseudo terminal as its
// standard input, and its prompts and errors on it.
type ptyInit struct {
	t              *testing.T
	p              *pty
	name           string // of the command, such as "whiteroom init"
	asks           string // what every prompt has, and nothing else that the command prints
	stdout, stderr *syncBuffer
	done           chan int
	cancel         context.CancelFunc
	code           int
	exited         bool
}

func startPTYInit(t *testing.T, p *pty, args ...string) *ptyInit {
	t.Helper()
	return startPTYCommand(t, p, "whiteroom init", "Password", append([]string{"init"}, args...)...)
}

// startPTYCommand runs the command with args, whose name is name and whose prompts have asks.
func startPTYCommand(t *testing.T, p *pty, name, asks string, args ...string) *ptyInit {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	r := &ptyInit{t: t, p: p, name: name, asks: asks, stdout: new(syncBuffer), stderr: new(syncBuffer), done: make(chan int, 1),
		cancel: cancel}
	e := testEnv(p.slave, r.stdout, io.MultiWriter(r.stderr, p.slave))
	go func() { r.done <- run(ctx, args, e) }()
	return r
}

// prompt waits for the n-th prompt, and for the terminal to be ready for the password, which is to stop echoing.
func (r *ptyInit) prompt(n int) {
	r.t.Helper()
	waitFor(r.t, fmt.Sprintf("prompt %d", n), func() bool { return strings.Count(r.stderr.String(), r.asks) >= n })
	r.p.waitNoEcho()
}

// answer types text at the n-th prompt.
func (r *ptyInit) answer(n int, text string) {
	r.t.Helper()
	r.prompt(n)
	r.p.typeText(text + "\r")
}

// exit waits for the command to return, which it must do in 15 s, and returns its exit code.
func (r *ptyInit) exit() int {
	r.t.Helper()
	if r.exited {
		return r.code
	}
	select {
	case r.code = <-r.done:
		r.exited = true
	case <-time.After(15 * time.Second):
		r.cancel()
		r.t.Fatalf("%s has not returned in 15 s, with stderr:\n%s", r.name, r.stderr)
	}
	return r.code
}

// finish waits for the command to return, and checks its exit code, its stdout, its stderr, which the terminal must
// have shown, and no more, and that the terminal is as it was before. None of the secrets may be in what the terminal
// showed, in the output or in the data directory.
func (r *ptyInit) finish(before syscall.Termios, data string, code int, stdout, stderr string, secrets ...string) {
	r.t.Helper()
	if got := r.exit(); got != code {
		r.t.Errorf("%s = %d, want %d, with stderr:\n%s", r.name, got, code, r.stderr)
	}
	if r.stdout.String() != stdout {
		r.t.Errorf("%s printed on stdout:\n%s\nwant:\n%s", r.name, r.stdout, stdout)
	}
	if r.stderr.String() != stderr {
		r.t.Errorf("%s printed on stderr:\n%s\nwant:\n%s", r.name, r.stderr, stderr)
	}
	// Echo comes before the text that follows it, so that the terminal shows exactly this only if nothing was echoed.
	waitFor(r.t, "the terminal to show what "+r.name+" printed", func() bool { return r.p.show() == r.stderr.String() })
	if got := r.p.termios(); got != before {
		r.t.Errorf("the terminal is %+v, want it as it was, %+v", got, before)
	}
	for _, secret := range secrets {
		checkNoSecret(r.t, "the terminal", secret, r.stdout.String(), r.p.show(), data)
	}
}

const (
	promptTwice = "Password: \nPassword (again): \n"
	tooShort    = "whiteroom init: the password: too short: 6 characters (min 15)\n"
	notSame     = "whiteroom init: the two passwords are not the same\n"
	shortWord   = "qwerty"
	otherWord   = "mismatched passphrase"
)

// TestIsTerminal checks that a pseudo terminal is a terminal, and that a file, a pipe and the null device are not.
func TestIsTerminal(t *testing.T) {
	p := openPTY(t)
	if !isTerminal(p.slave) {
		t.Error("the slave of a pseudo terminal is not a terminal")
	}
	if !newPasswordReader(p.slave, io.Discard).terminal() {
		t.Error("a password reader of the slave of a pseudo terminal does not read a terminal")
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pr.Close(); _ = pw.Close() }()
	for name, f := range map[string]*os.File{"a file": file, "the null device": null, "a pipe": pr} {
		if isTerminal(f) {
			t.Errorf("%s is a terminal", name)
		}
		if newPasswordReader(f, io.Discard).terminal() {
			t.Errorf("a password reader of %s reads a terminal", name)
		}
	}
}

// TestReadHidden checks the line that readHidden reads without echo: that a line over the limit is read to its end and
// then is errTooLong, so that the next line is the next one, that the terminal is as it was after each, and that a
// context that is done reads nothing.
func TestReadHidden(t *testing.T) {
	p := openPTY(t)
	before := p.termios()
	read := func(ctx context.Context, limit int) ([]byte, error) { return readHidden(ctx, p.slave, limit) }
	type result struct {
		line []byte
		err  error
	}
	start := func(limit int) <-chan result {
		ch := make(chan result, 1)
		go func() {
			line, err := read(t.Context(), limit)
			ch <- result{line, err}
		}()
		return ch
	}
	finish := func(ch <-chan result) result {
		t.Helper()
		select {
		case r := <-ch:
			return r
		case <-time.After(10 * time.Second):
			t.Fatal("readHidden has not returned in 10 s")
			return result{}
		}
	}

	ch := start(10)
	p.waitNoEcho()
	p.typeText("0123456789abcdef\r")
	if r := finish(ch); r.line != nil || !errors.Is(r.err, errTooLong) {
		t.Errorf("readHidden of a line over the limit = %q, %v; want %v", r.line, r.err, errTooLong)
	}
	if got := p.termios(); got != before {
		t.Errorf("after a line that is too long, the terminal is %+v, want %+v", got, before)
	}
	ch = start(10)
	p.waitNoEcho()
	p.typeText("next\r")
	if r := finish(ch); string(r.line) != "next" || r.err != nil {
		t.Errorf("readHidden of the next line = %q, %v; want next", r.line, r.err)
	}
	ch = start(10)
	p.waitNoEcho()
	p.typeText("0123456789\r")
	if r := finish(ch); string(r.line) != "0123456789" || r.err != nil {
		t.Errorf("readHidden of a line of the limit = %q, %v; want it", r.line, r.err)
	}
	if got := p.termios(); got != before {
		t.Errorf("after the lines, the terminal is %+v, want %+v", got, before)
	}
	if shown := p.show(); strings.Contains(shown, "0123") || strings.Contains(shown, "next") {
		t.Errorf("the terminal shows the lines: %q", shown)
	}

	done, cancel := context.WithCancel(t.Context())
	cancel()
	if line, err := read(done, 10); line != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("readHidden with a context that is done = %q, %v; want %v", line, err, context.Canceled)
	}
	if got := p.termios(); got != before {
		t.Errorf("after a context that is done, the terminal is %+v, want %+v", got, before)
	}
}

// TestInitTerminal makes data directories with the password typed at a pseudo terminal, which asks twice, without echo, and
// again if the two are not the same or the password is refused; and the failures and the interrupts, after which the
// terminal is as it was and the data directory is not made.
func TestInitTerminal(t *testing.T) {
	// start opens a terminal and a whiteroom init of a data directory that is missing, and returns the data directory, the
	// terminal as it is, the command and the arguments after the flags of the administrator that it was given.
	start := func(t *testing.T, extra ...string) (data string, before syscall.Termios, r *ptyInit) {
		t.Helper()
		data = filepath.Join(t.TempDir(), "data")
		p := openPTY(t)
		before = p.termios()
		return data, before, startPTYInit(t, p, adminFlags(append([]string{"-data", data}, extra...)...)...)
	}
	made := func(data string) string {
		return "created the data directory " + data + "\ncreated the administrator " + initLogin + " ("
	}
	// succeeded checks that the data directory is made, with the password, and with the first administrator.
	succeeded := func(t *testing.T, r *ptyInit, before syscall.Termios, data, stderr string, secrets ...string) {
		t.Helper()
		r.exit()
		got := inspect(t, data, initPassword)
		admin := checkAdmin(t, "the terminal", got)
		r.finish(before, data, 0, made(data)+admin.ID+")\n", stderr, append(secrets, initPassword)...)
		if len(got.docs) != 0 {
			t.Errorf("the store has documents")
		}
	}

	t.Run("the password twice", func(t *testing.T) {
		data, before, r := start(t)
		r.answer(1, initPassword)
		r.answer(2, initPassword)
		succeeded(t, r, before, data, promptTwice)
	})
	t.Run("the two are not the same", func(t *testing.T) {
		data, before, r := start(t)
		r.answer(1, initPassword)
		r.answer(2, otherWord)
		r.answer(3, initPassword)
		r.answer(4, initPassword)
		succeeded(t, r, before, data, promptTwice+notSame+promptTwice, otherWord)
	})
	t.Run("a password that is refused", func(t *testing.T) {
		data, before, r := start(t)
		r.answer(1, shortWord)
		r.answer(2, shortWord)
		r.answer(3, initPassword)
		r.answer(4, initPassword)
		succeeded(t, r, before, data, promptTwice+tooShort+promptTwice, shortWord)
	})
	t.Run("a mismatch and a password that is refused", func(t *testing.T) {
		data, before, r := start(t)
		r.answer(1, shortWord)
		r.answer(2, otherWord)
		r.answer(3, shortWord)
		r.answer(4, shortWord)
		r.answer(5, initPassword)
		r.answer(6, initPassword)
		succeeded(t, r, before, data, promptTwice+notSame+promptTwice+tooShort+promptTwice, shortWord, otherWord)
	})

	t.Run("three entries that are not the same", func(t *testing.T) {
		data, before, r := start(t)
		for n := 1; n <= 6; n += 2 {
			r.answer(n, initPassword)
			r.answer(n+1, otherWord)
		}
		r.finish(before, data, 1, "", promptTwice+notSame+promptTwice+notSame+promptTwice+notSame+
			"whiteroom init: no password was accepted in 3 tries\n", initPassword, otherWord)
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("the data directory is made:\n%s", s)
		}
	})
	t.Run("three passwords that are refused", func(t *testing.T) {
		data, before, r := start(t)
		for n := 1; n <= 6; n += 2 {
			r.answer(n, shortWord)
			r.answer(n+1, shortWord)
		}
		r.finish(before, data, 1, "", promptTwice+tooShort+promptTwice+tooShort+promptTwice+tooShort+
			"whiteroom init: no password was accepted in 3 tries\n", shortWord)
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("the data directory is made:\n%s", s)
		}
	})
	t.Run("a valid password and a flag that is refused", func(t *testing.T) {
		data, before, r := start(t, "-email", "nobody")
		r.answer(1, initPassword)
		r.answer(2, initPassword)
		r.finish(before, data, 1, "", promptTwice+emailLine(), initPassword)
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("the data directory is made:\n%s", s)
		}
	})
	t.Run("a flag and a password that are refused", func(t *testing.T) {
		data, before, r := start(t, "-email", "nobody")
		r.answer(1, shortWord)
		r.answer(2, shortWord)
		r.finish(before, data, 1, "", promptTwice+emailLine()+tooShort, shortWord)
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("the data directory is made:\n%s", s)
		}
	})
	t.Run("the end of the input", func(t *testing.T) {
		data, before, r := start(t)
		r.prompt(1)
		r.p.typeText("\x04")
		r.finish(before, data, 1, "", "Password: \nwhiteroom init: no password was read from the standard input\n")
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("the data directory is made:\n%s", s)
		}
	})

	t.Run("an interrupt at the first prompt", func(t *testing.T) {
		data, before, r := start(t)
		r.prompt(1)
		r.cancel()
		r.finish(before, data, 1, "", "Password: \nwhiteroom init: interrupted\n")
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("the data directory is made:\n%s", s)
		}
	})
	t.Run("an interrupt at the second prompt", func(t *testing.T) {
		data, before, r := start(t)
		r.answer(1, initPassword)
		r.prompt(2)
		r.cancel()
		r.finish(before, data, 1, "", "Password: \nPassword (again): \nwhiteroom init: interrupted\n", initPassword)
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("the data directory is made:\n%s", s)
		}
	})
	t.Run("an interrupt once the store is made", func(t *testing.T) {
		data, before, r := start(t)
		r.answer(1, shortWord)
		r.answer(2, shortWord)
		r.prompt(3)
		r.cancel()
		r.finish(before, data, 1, "", promptTwice+tooShort+"Password: \nwhiteroom init: interrupted\n", shortWord)
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("the data directory is made:\n%s", s)
		}
	})
}

const (
	resetPrompts  = "New password for " + initLogin + ": \nNew password for " + initLogin + " (again): \n"
	resetTooShort = "whiteroom account reset: the password: too short: 6 characters (min 15)\n"
	resetNotSame  = "whiteroom account reset: the two passwords are not the same\n"
)

// TestAccountResetTerminal resets a password typed at a pseudo terminal, which asks twice, without echo, and again if the
// two are not the same or the password is refused, 3 times at most; after a failure the terminal is as it was and the
// account is not changed.
func TestAccountResetTerminal(t *testing.T) {
	// start makes a data directory with the first administrator, and opens a terminal and a whiteroom account reset for it,
	// and returns the data directory, the account, the logs as they are, the terminal as it is and the command.
	start := func(t *testing.T) (data string, admin account.Account, logs logRecords, before syscall.Termios, r *ptyInit) {
		t.Helper()
		data = accountData(t)
		admin = mustFind(t, data, initLogin)
		logs = logsOf(t, data)
		p := openPTY(t)
		before = p.termios()
		return data, admin, logs, before, startPTYCommand(t, p, "whiteroom account reset", "New password for", "account", "reset",
			"-data", data, initLogin)
	}
	// succeeded checks that the password is reset, and that the terminal and the data directory have none of the secrets.
	succeeded := func(t *testing.T, r *ptyInit, data string, admin account.Account, before syscall.Termios, stderr string, secrets ...string) {
		t.Helper()
		r.exit()
		r.finish(before, data, 0, "reset the password of "+admin.Login+" ("+admin.ID+")\n", stderr, append(secrets, changedPassword)...)
		checkLogin(t, "after the command", data, initLogin, changedPassword)
	}
	// unchanged checks that a command that failed has left the account and the logs as they were.
	unchanged := func(t *testing.T, r *ptyInit, data string, logs logRecords, before syscall.Termios, stderr string, secrets ...string) {
		t.Helper()
		r.finish(before, data, 1, "", stderr, secrets...)
		checkLogs(t, "after the command", data, logs)
		checkLogin(t, "after the command", data, initLogin, initPassword)
	}

	t.Run("the password twice", func(t *testing.T) {
		data, admin, _, before, r := start(t)
		r.answer(1, changedPassword)
		r.answer(2, changedPassword)
		succeeded(t, r, data, admin, before, resetPrompts)
	})
	t.Run("the two are not the same", func(t *testing.T) {
		data, admin, _, before, r := start(t)
		r.answer(1, changedPassword)
		r.answer(2, otherWord)
		r.answer(3, changedPassword)
		r.answer(4, changedPassword)
		succeeded(t, r, data, admin, before, resetPrompts+resetNotSame+resetPrompts, otherWord)
	})
	t.Run("a password that is refused", func(t *testing.T) {
		data, admin, _, before, r := start(t)
		r.answer(1, shortWord)
		r.answer(2, shortWord)
		r.answer(3, changedPassword)
		r.answer(4, changedPassword)
		succeeded(t, r, data, admin, before, resetPrompts+resetTooShort+resetPrompts, shortWord)
	})
	t.Run("three passwords that are refused", func(t *testing.T) {
		data, _, logs, before, r := start(t)
		for n := 1; n <= 6; n += 2 {
			r.answer(n, shortWord)
			r.answer(n+1, shortWord)
		}
		unchanged(t, r, data, logs, before, resetPrompts+resetTooShort+resetPrompts+resetTooShort+resetPrompts+resetTooShort+
			"whiteroom account reset: no password was accepted in 3 tries\n", shortWord)
	})
	t.Run("three entries that are not the same", func(t *testing.T) {
		data, _, logs, before, r := start(t)
		for n := 1; n <= 6; n += 2 {
			r.answer(n, changedPassword)
			r.answer(n+1, otherWord)
		}
		unchanged(t, r, data, logs, before, resetPrompts+resetNotSame+resetPrompts+resetNotSame+resetPrompts+resetNotSame+
			"whiteroom account reset: no password was accepted in 3 tries\n", changedPassword, otherWord)
	})
	t.Run("an interrupt at the prompt", func(t *testing.T) {
		data, _, logs, before, r := start(t)
		r.prompt(1)
		r.cancel()
		unchanged(t, r, data, logs, before, "New password for "+initLogin+": \nwhiteroom account reset: interrupted\n")
	})
}
