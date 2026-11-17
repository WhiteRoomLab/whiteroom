package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
)

// processWait is how long a test of processes waits for what a process does: a start, a stop, a command.
const processWait = 60 * time.Second

// proc is a process that a test of processes has started: the command whiteroom or whiteroom-db with arguments.
type proc struct {
	name           string
	cmd            *exec.Cmd
	stdout, stderr *syncBuffer
	done           chan struct{} // closed when the process has ended and its output is whole
	waitErr        error         // of Wait, once done is closed
}

// startProc starts the executable bin with args, with the standard input stdin, which may be nil, and no environment but the
// directory of the temporary files. The process is killed when the test ends if it still runs, and when the test process dies.
func startProc(t *testing.T, name, bin, tmp string, stdin io.Reader, args ...string) *proc {
	t.Helper()
	return startProcTo(t, nil, name, bin, tmp, stdin, args...)
}

// startProcTo is startProc with the standard error stderr, if it is not nil, instead of the buffer, which stays empty.
func startProcTo(t *testing.T, stderr *os.File, name, bin, tmp string, stdin io.Reader, args ...string) *proc {
	t.Helper()
	p := &proc{name: name + " " + strings.Join(args, " "), stdout: new(syncBuffer), stderr: new(syncBuffer), done: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = stdin, p.stdout, p.stderr
	if stderr != nil {
		p.cmd.Stderr = stderr // a file, which the process writes itself, with no copy by this process
	}
	p.cmd.Env = []string{"TMPDIR=" + tmp}
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("%s: %v", p.name, err)
	}
	go func() {
		p.waitErr = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(processWait):
			t.Errorf("%s has not ended in %v after it was killed", p.name, processWait)
		}
	})
	return p
}

// pid returns the process ID.
func (p *proc) pid() int { return p.cmd.Process.Pid }

// exited waits for the process to end, and returns its exit code.
func (p *proc) exited(t *testing.T) int {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(processWait):
		t.Fatalf("%s has not ended in %v\nstdout:\n%s\nstderr:\n%s", p.name, processWait, p.stdout, p.stderr)
	}
	return p.cmd.ProcessState.ExitCode()
}

// firstLine waits for the process to print a first line on its standard output, and returns it with its line break.
func (p *proc) firstLine(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(processWait)
	for {
		if line, _, ok := strings.Cut(p.stdout.String(), "\n"); ok {
			return line + "\n"
		}
		select {
		case <-p.done:
			t.Fatalf("%s has ended with %d before it has printed a line\nstdout:\n%s\nstderr:\n%s", p.name, p.cmd.ProcessState.ExitCode(),
				p.stdout, p.stderr)
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s has printed no line in %v\nstderr:\n%s", p.name, processWait, p.stderr)
		}
	}
}

// stop sends SIGTERM to the process, and checks that it ends with 0.
func (p *proc) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("%s: %v", p.name, err)
	}
	if code := p.exited(t); code != 0 {
		t.Errorf("%s ended with %d after SIGTERM, want 0\nstdout:\n%s\nstderr:\n%s", p.name, code, p.stdout, p.stderr)
	}
}

// brokenPipe returns the end of a pipe that a process writes, whose reader has already gone: a write to it fails with
// EPIPE, and raises SIGPIPE.
func brokenPipe(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// buildExecutables builds whiteroom and whiteroom-db into the directory dir with one go build, which the race detector is in
// when the test is.
func buildExecutables(t *testing.T, dir, tmp string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	args := []string{"build", "-o", dir + string(filepath.Separator)}
	if raceBuild {
		args = append(args, "-race")
	}
	cmd := exec.CommandContext(ctx, "go", append(args, "./cmd/whiteroom", "./cmd/whiteroom-db")...)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build of whiteroom and whiteroom-db: %v\n%s", err, out)
	}
}

// openFiles returns where the open files of the process with the ID pid point to, as /proc has them.
func openFiles(t *testing.T, pid int) []string {
	t.Helper()
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // the file that ReadDir itself had open
		}
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target)
	}
	return targets
}

// checkNoOpenIn checks that no open file of the process p is in the directory dir, or is dir.
func checkNoOpenIn(t *testing.T, what string, p *proc, dir string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := openFiles(t, p.pid())
	if len(files) == 0 {
		t.Fatalf("%s: the process has no open file at all", what)
	}
	for _, target := range files {
		for _, d := range []string{dir, resolved} {
			if target == d || strings.HasPrefix(target, d+string(filepath.Separator)) {
				t.Errorf("%s: the process has %s open, in the data directory %s", what, target, dir)
			}
		}
	}
}

// socketClient returns a client of the admin API that listens on the Unix socket at path, as the origin http://localhost.
func socketClient(t *testing.T, path string, stderr *syncBuffer) *adminProc {
	t.Helper()
	a := &adminProc{
		origin: "http://localhost", base: "http://localhost", network: "unix", addr: path, stderr: stderr,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       processWait,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return new(net.Dialer).DialContext(ctx, "unix", path)
				},
			},
		},
	}
	t.Cleanup(a.client.CloseIdleConnections)
	return a
}

// headLines returns the lines of text that are heads of the audit logs, as the daemon writes them to its standard error.
func headLines(text string) []string {
	var lines []string
	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, headPrefix) {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestProcesses runs the two executables as processes, with one config file that both read, through what the issue asks for:
// whiteroom-db makes the data directory and serves it; whiteroom init imports the sample site and makes the administrator,
// with the password on a pipe; whiteroom serve admin connects to the daemon and listens on a Unix socket; a login, a page that
// the API saves and publishes, and the build that follows; whiteroom build into another output directory; whiteroom backup;
// a second daemon on the copy, which whiteroom doctor checks with the heads that backup printed; whiteroom doctor with the
// heads that the first daemon has written to its standard error; and the daemon and the two servers, each with a standard
// error that is a pipe that no one reads (#130). The server has no file of the data directory open and writes no head.
// Every process ends with 0 after SIGTERM.
func TestProcesses(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go tool is not in PATH, which builds the executables")
	}
	dir := shortDir(t)
	tmp, procTmp := filepath.Join(dir, "tmp"), filepath.Join(dir, "proc-tmp")
	for _, d := range []string{tmp, procTmp} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	buildExecutables(t, bin, procTmp)
	whiteroom, whiteroomDB := filepath.Join(bin, "whiteroom"), filepath.Join(bin, "whiteroom-db")

	data, run, run2 := filepath.Join(dir, "data"), filepath.Join(dir, "run"), filepath.Join(dir, "run2")
	copyDir, adminSock := filepath.Join(dir, "copy"), filepath.Join(dir, "admin.sock")
	out, out2 := filepath.Join(dir, "out"), filepath.Join(dir, "out2")
	site := copySample(t)
	for _, d := range []string{run, run2} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// What both executables read: the second daemon and the second output directory are given by flags.
	cfg := writeConfig(t, dir, "whiteroom.yaml", strings.Join([]string{
		"data: " + strconv.Quote(data), "db: " + strconv.Quote(run), "site: " + strconv.Quote(site), "out: " + strconv.Quote(out),
		"cache: " + strconv.Quote(filepath.Join(dir, "cache")), "tmp: " + strconv.Quote(tmp),
		"socket: " + strconv.Quote(adminSock), "origin: http://localhost",
	}, "\n")+"\n")

	// One command to the end, with no standard input and no secret.
	command := func(name, bin string, stdin io.Reader, args ...string) (stdout, stderr string) {
		t.Helper()
		p := startProc(t, name, bin, procTmp, stdin, args...)
		if code := p.exited(t); code != 0 {
			t.Fatalf("%s ended with %d\nstdout:\n%s\nstderr:\n%s", p.name, code, p.stdout, p.stderr)
		}
		return p.stdout.String(), p.stderr.String()
	}

	stdout, _ := command("whiteroom-db", whiteroomDB, nil, "init", "-config", cfg)
	if want := "created the data directory " + data + "\n"; stdout != want {
		t.Errorf("whiteroom-db init printed %q, want %q", stdout, want)
	}
	daemon := startProc(t, "whiteroom-db", whiteroomDB, procTmp, nil, "serve", "-config", cfg)
	if line := daemon.firstLine(t); !strings.HasPrefix(line, "serving the data directory "+data+" on the sockets in "+run+" (writer: user ") {
		t.Fatalf("whiteroom-db serve printed %q first", line)
	}

	// init: the password is the first line of a pipe, never an argument or in the environment.
	stdout, stderr := command("whiteroom", whiteroom, strings.NewReader(initPassword+"\n"), append([]string{"init", "-config", cfg},
		adminFlags(site)...)...)
	if !strings.Contains(stdout, "initialized the store of "+run+"\n") || !strings.Contains(stdout, "imported the site "+site+"\n") {
		t.Errorf("whiteroom init printed:\n%s\nand on stderr:\n%s\nwant that it has initialized the store and imported the site", stdout, stderr)
	}
	checkNoSecret(t, "whiteroom init", initPassword, stdout, stderr, data)

	// serve admin on its socket.
	admin := startProc(t, "whiteroom", whiteroom, procTmp, nil, "serve", "admin", "-config", cfg)
	if want := "serving admin of the store of " + run + " at http://localhost (listening on socket " + adminSock + ")\n"; admin.firstLine(t) != want {
		t.Fatalf("whiteroom serve admin printed %q first, want %q", admin.firstLine(t), want)
	}
	api := socketClient(t, adminSock, admin.stderr)
	cookie := api.login(t)
	if built := api.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the start build has %+v, want a release\nstderr:\n%s", built.Last, admin.stderr)
	}
	checkNoOpenIn(t, "whiteroom serve admin after its start build", admin, data)

	// A page that the API saves and publishes, and the build that takes it.
	id := api.publishPage(t, cookie, "en", "Published by the processes", "processes")
	built := api.waitPublished(t, cookie, id)
	if failedBuild(built) || built.Last.Release == "" || len(built.Last.Errors) != 0 {
		t.Fatalf("the build of the publishing has %+v, want a release\nstderr:\n%s", built.Last, admin.stderr)
	}
	if got := currentOf(t, out); got != built.Last.Release {
		t.Errorf("the build says that it has published %s, and current is %s", built.Last.Release, got)
	}
	if _, err := os.Stat(filepath.Join(out, "current", "public", "en", "processes", "index.html")); err != nil {
		t.Errorf("the page is not in the output directory: %v", err)
	}
	checkNoOpenIn(t, "whiteroom serve admin after the page", admin, data)

	// A build from the store into another output directory is the release of the server.
	stdout, _ = command("whiteroom", whiteroom, nil, "build", "-config", cfg, "-out", out2, "-cache", filepath.Join(dir, "cache2"))
	if got := currentOf(t, out2); got != built.Last.Release || !strings.HasPrefix(stdout, "published release "+got+" in "+out2+": ") {
		t.Errorf("whiteroom build -out printed %q and published %s, want the release %s of the server", stdout, got, built.Last.Release)
	}

	// backup, and a second daemon on the copy, which doctor checks with the heads that backup printed.
	printed, _ := command("whiteroom", whiteroom, nil, "backup", "-config", cfg, copyDir)
	if heads := printedHeads(t, "whiteroom backup", run, copyDir, printed); len(heads) != 2 {
		t.Errorf("whiteroom backup printed the heads %v, want those of the 2 logs", heads)
	}
	second := startProc(t, "whiteroom-db", whiteroomDB, procTmp, nil, "serve", "-config", cfg, "-data", copyDir, "-db", run2)
	if line := second.firstLine(t); !strings.HasPrefix(line, "serving the data directory "+copyDir+" on the sockets in "+run2+" (") {
		t.Fatalf("the second whiteroom-db serve printed %q first", line)
	}
	stdout, stderr = command("whiteroom", whiteroom, strings.NewReader(printed), "doctor", "-config", cfg, "-db", run2, "-heads", "-")
	if want := "no problems found in " + run2 + "\n"; stdout != want || stderr != "" {
		t.Errorf("whiteroom doctor on the copy, with the heads of the backup, printed %q, and on stderr %q, want %q", stdout, stderr, want)
	}

	// The server stops: its standard error has no head, nor anything else.
	admin.stop(t)
	if got := admin.stderr.String(); got != "" {
		t.Errorf("whiteroom serve admin printed on stderr:\n%s\nwant nothing: the heads are the daemon's", got)
	}
	if got := admin.stdout.String(); got != "serving admin of the store of "+run+" at http://localhost (listening on socket "+adminSock+")\n" {
		t.Errorf("whiteroom serve admin printed on stdout:\n%s", got)
	}
	checkNoSecret(t, "whiteroom serve admin", initPassword, admin.stdout.String(), admin.stderr.String(), data)

	// The first daemon stops, and writes the heads of the audit logs, which it has not yet, to its standard error.
	daemon.stop(t)
	var heads []audit.Head
	for _, line := range headLines(daemon.stderr.String()) {
		h, err := audit.ParseHead(strings.TrimSuffix(line, "\n"))
		if err != nil {
			t.Errorf("whiteroom-db serve wrote the line %q, which is not a head: %v", line, err)
			continue
		}
		heads = append(heads, h)
	}
	if got := lastHeads(heads); len(got) != 2 {
		t.Errorf("whiteroom-db serve wrote the heads of %d logs to its standard error, want 2:\n%s", len(got), daemon.stderr)
	}
	checkNoSecret(t, "whiteroom-db serve", initPassword, daemon.stdout.String(), daemon.stderr.String(), data)

	// The daemon again, on the same data directory: doctor with the lines of its standard error, as the journal has them.
	again := startProc(t, "whiteroom-db", whiteroomDB, procTmp, nil, "serve", "-config", cfg)
	again.firstLine(t)
	stdout, stderr = command("whiteroom", whiteroom, strings.NewReader(daemon.stderr.String()), "doctor", "-config", cfg, "-heads", "-")
	if want := "no problems found in " + run + "\n"; stdout != want || stderr != "" {
		t.Errorf("whiteroom doctor with the heads that the daemon wrote printed %q, and on stderr %q, want %q", stdout, stderr, want)
	}
	again.stop(t)

	// The servers outlive the reader of their standard error, which has gone (#130): the daemon writes the heads to it when it
	// starts and when it stops, and the server logs that the daemon cannot be reached, which writing to such a pipe on the
	// standard error would end, as SIGPIPE does by default. Each goes on serving, and ends with 0. What they write to the
	// standard error is lost, also for the messages of a failure here.
	quiet := startProcTo(t, brokenPipe(t), "whiteroom-db", whiteroomDB, procTmp, nil, "serve", "-config", cfg)
	quiet.firstLine(t)
	quietAdmin := startProcTo(t, brokenPipe(t), "whiteroom", whiteroom, procTmp, nil, "serve", "admin", "-config", cfg)
	quietAdmin.firstLine(t)
	quietAPI := socketClient(t, adminSock, quietAdmin.stderr)
	quietAPI.login(t)
	quiet.stop(t)
	if status, _ := quietAPI.tryLogin(t, initLogin, initPassword); status != http.StatusServiceUnavailable {
		t.Errorf("a login while the daemon is away has the status %d, want %d", status, http.StatusServiceUnavailable)
	}
	quietAdmin.stop(t)
	// serve public logs a link that leaves the public directory, which it does not follow.
	if err := os.Symlink(cfg, filepath.Join(out, "current", "public", "escape.html")); err != nil {
		t.Fatal(err)
	}
	quietPublic := startProcTo(t, brokenPipe(t), "whiteroom", whiteroom, procTmp, nil, "serve", "public", "-addr", "127.0.0.1:0", out)
	_, publicURL, _ := strings.Cut(strings.TrimSuffix(quietPublic.firstLine(t), "\n"), " at ")
	client := &http.Client{Timeout: processWait}
	for _, tt := range []struct {
		path   string
		status int
	}{{"escape.html", http.StatusInternalServerError}, {"", http.StatusOK}} {
		res, err := client.Get(publicURL + tt.path)
		if err != nil {
			t.Fatalf("GET %s%s from whiteroom serve public: %v", publicURL, tt.path, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tt.status {
			t.Errorf("GET %s%s from whiteroom serve public has the status %d, want %d", publicURL, tt.path, res.StatusCode, tt.status)
		}
	}
	client.CloseIdleConnections()
	quietPublic.stop(t)
	second.stop(t)
	for _, p := range []*proc{daemon, again, second} {
		if strings.Contains(p.stderr.String(), "DATA RACE") {
			t.Errorf("%s reports a race:\n%s", p.name, p.stderr)
		}
	}

	for _, path := range []string{adminSock, filepath.Join(run, "writer.sock"), filepath.Join(run, "reader.sock"),
		filepath.Join(run2, "writer.sock"), filepath.Join(run2, "reader.sock")} {
		noFile(t, "after every process has stopped", path)
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Errorf("the directory of the temporary files has %d entries after every process has stopped (%v), want none", len(entries), err)
	}
	checkUnlocked(t, "after the processes", data)
}
