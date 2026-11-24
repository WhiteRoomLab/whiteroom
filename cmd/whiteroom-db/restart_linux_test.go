//go:build linux

package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// TestRestartRefused checks that serve, compact, gc, accept-break and repair (with and without -apply) refuse a data directory
// whose mark of a failed sync (concept section 3.5) has the boot ID of this machine, with the sentence of the mark and without
// a change, and that they remove the mark of an earlier boot and say so, but for repair without -apply, which leaves it where
// it is.
func TestRestartRefused(t *testing.T) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Skipf("no boot ID: %v", err)
	}
	machine := strings.TrimSuffix(string(b), "\n")
	earlier := "00000000-0000-4000-8000-0000000000e1"
	if machine == earlier {
		earlier = "00000000-0000-4000-8000-0000000000e2"
	}

	const at, cause = "2026-11-23T08:49:00Z", "sync x/logdb/log/y.log: input/output error"
	d := newDirs(t)
	s := d.startServe(t, d.args()...)
	mustCommit(t, openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{}), row{2, "a", "1"})
	if code := s.stop(); code != 0 {
		t.Fatalf("whiteroom-db serve = %d", code)
	}
	mark := filepath.Join(d.data, "stopped")
	writeMark := func(boot string) {
		t.Helper()
		text := "whiteroom-db stopped after a sync failed; restart the machine before starting it again\n" +
			"boot: " + boot + "\ntime: " + at + "\ncause: " + cause + "\n"
		if err := os.WriteFile(mark, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	what := d.data + ": a sync failed at " + at + " (" + cause + ")"
	refusal := what + ", and the machine has not restarted since; restart the machine before starting whiteroom-db on this " +
		"directory again\n"
	removal := what + " before the machine restarted; the mark " + mark + " is removed: check the store with whiteroom doctor\n"

	// A directory of the sockets that no daemon has used: serve makes nothing in it when it refuses.
	fresh := filepath.Join(d.root, "s2")
	if err := os.Mkdir(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	d2 := d
	d2.socks = fresh

	writeMark(machine)
	before := files(t, d.data)
	keep := filepath.Join(d.root, "keep")
	for _, args := range [][]string{append([]string{"serve"}, d2.args()...), {"compact", "-data", d.data}, {"gc", "-data", d.data},
		maintenanceArgs("accept-break", d.data), {"repair", "-data", d.data}, {"repair", "-data", d.data, "-apply", "-keep", keep}} {
		code, stdout, stderr := runCmd(t, args...)
		if want := "whiteroom-db " + args[0] + ": " + refusal; code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db %s on a mark of this boot = %d, with stdout %q and stderr %q, want 1, nothing and %q", args[0],
				code, stdout, stderr, want)
		}
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("the refusals changed the data directory:\n%v\nwas\n%v", got, before)
	}
	if got, err := os.ReadDir(fresh); err != nil || len(got) != 0 {
		t.Errorf("the refused serve made %v (%v) in the directory of the sockets", got, err)
	}
	if _, err := os.Lstat(keep); !os.IsNotExist(err) {
		t.Errorf("the refused repair made %s: %v", keep, err)
	}

	// A mark of an earlier boot is removed, and the command says so on the standard error.
	for _, c := range []struct {
		command, stdout string
		args            []string
	}{
		{"compact", "compacted " + d.data + "\n", nil},
		{"gc", "collected garbage in " + d.data + ": 0 segments, 0 object files, 0 temporary files\n", nil},
		{"repair", "unreadable: event 1\nnothing to repair in " + d.data + "\n", []string{"-apply", "-keep", keep}},
	} {
		writeMark(earlier)
		code, stdout, stderr := runCmd(t, append([]string{c.command, "-data", d.data}, c.args...)...)
		if want := "whiteroom-db " + c.command + ": " + removal; code != 0 || stdout != c.stdout || stderr != want {
			t.Errorf("whiteroom-db %s on a mark of an earlier boot = %d, with stdout %q and stderr %q, want 0, %q and %q",
				c.command, code, stdout, stderr, c.stdout, want)
		}
		if _, err := os.Lstat(mark); !os.IsNotExist(err) {
			t.Errorf("the mark is still there after %s: %v", c.command, err)
		}
	}

	// repair without -apply leaves the mark of an earlier boot where it is, as check does.
	writeMark(earlier)
	wantMark, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := runCmd(t, "repair", "-data", d.data); code != 0 || stdout != "unreadable: event 1\nnothing to repair in "+d.data+"\n" || stderr != "" {
		t.Errorf("whiteroom-db repair on a mark of an earlier boot = %d, with stdout %q and stderr %q, want 0 and nothing to repair", code, stdout, stderr)
	}
	if got, err := os.ReadFile(mark); err != nil || string(got) != string(wantMark) {
		t.Errorf("the mark is %q (%v) after repair without -apply, want it as it was", got, err)
	}

	writeMark(earlier)
	s = d.startServe(t, d.args()...)
	if got, want := s.stdout.String(), d.startLine(os.Getuid()); got != want {
		t.Errorf("the start line is %q, want %q", got, want)
	}
	if got := lines(s.stderr); !slices.ContainsFunc(got, func(line string) bool {
		return strings.HasSuffix(line, " "+strings.TrimSuffix(removal, "\n"))
	}) {
		t.Errorf("the log of serve is %q, want the removal of the mark", got)
	}
	if _, err := os.Lstat(mark); !os.IsNotExist(err) {
		t.Errorf("the mark is still there after serve started: %v", err)
	}
	if v, found := value(t, openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{}), "a"); !found || v != "1" {
		t.Errorf("the daemon serves %q, %v after the removal, want 1", v, found)
	}
	if code := s.stop(); code != 0 {
		t.Errorf("whiteroom-db serve = %d", code)
	}
}

// TestRootNotWritableRefused checks that serve, compact, gc, accept-break and repair -apply refuse a data directory whose root the
// user cannot write in, although logdb/ and files/ are writable (concept section 3.5): the mark of a failed sync could not be
// left there. They say so on the standard error, change nothing, and make no socket, and the directory serves once it is given
// back. repair without -apply works on it, as check does.
func TestRootNotWritableRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser passes access(2)")
	}
	d := newDirs(t)
	s := d.startServe(t, d.args()...)
	mustCommit(t, openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{}), row{2, "a", "1"})
	if code := s.stop(); code != 0 {
		t.Fatalf("whiteroom-db serve = %d", code)
	}
	fresh := filepath.Join(d.root, "s2")
	if err := os.Mkdir(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	d2 := d
	d2.socks = fresh

	if err := os.Chmod(d.data, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d.data, 0o700) })
	before := files(t, d.data)
	refusal := d.data + ": whiteroom-db cannot write in the data directory (permission denied); it must be able to leave the " +
		"mark of a failed sync there: give the directory to the user of the daemon\n"
	keep := filepath.Join(d.root, "keep")
	for _, args := range [][]string{append([]string{"serve"}, d2.args()...), {"compact", "-data", d.data}, {"gc", "-data", d.data},
		maintenanceArgs("accept-break", d.data), {"repair", "-data", d.data, "-apply", "-keep", keep}} {
		code, stdout, stderr := runCmd(t, args...)
		if want := "whiteroom-db " + args[0] + ": " + refusal; code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db %s on a directory that it cannot write in = %d, with stdout %q and stderr %q, want 1, nothing "+
				"and %q", args[0], code, stdout, stderr, want)
		}
	}
	if code, stdout, stderr := runCmd(t, "repair", "-data", d.data); code != 0 || stdout != "unreadable: event 1\nnothing to repair in "+d.data+"\n" || stderr != "" {
		t.Errorf("whiteroom-db repair on a directory that it cannot write in = %d, with stdout %q and stderr %q, want 0 and nothing to repair",
			code, stdout, stderr)
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("the refusals changed the data directory:\n%v\nwas\n%v", got, before)
	}
	if _, err := os.Lstat(keep); !os.IsNotExist(err) {
		t.Errorf("the refused repair made %s: %v", keep, err)
	}
	if got, err := os.ReadDir(fresh); err != nil || len(got) != 0 {
		t.Errorf("the refused serve made %v (%v) in the directory of the sockets", got, err)
	}

	if err := os.Chmod(d.data, 0o700); err != nil {
		t.Fatal(err)
	}
	s = d.startServe(t, d.args()...)
	if v, found := value(t, openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{}), "a"); !found || v != "1" {
		t.Errorf("the daemon serves %q, %v after the directory is given back, want 1", v, found)
	}
	if code := s.stop(); code != 0 {
		t.Errorf("whiteroom-db serve = %d", code)
	}
}
