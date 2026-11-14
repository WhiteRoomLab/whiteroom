//go:build unix

package store

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/dmlog"
)

// TestWorkloadPhases runs the workload of the recording (TestDMLogWorkload) without a recording, in a directory of the
// OS, and answers its marks as record.sh does: the processes of the phases that kill themselves die, and the others
// end; the marks come in an order that the check of a recording takes (dmlogPoint.see), and tell at the end of the
// last Update, of the copy of Backup of the store of the Update 160, and of the generation 3; the marks other than
// the acks of the Updates are those of the phases: what each process that opens the store sees after the process
// before it has died; the GC of the three maintenances removes the file of the Update that the third process has
// not ended, the segment of the generation that the first compaction has not ended, and the segments of the
// generation 1; and the store and the copy are those of the workload then. The workload does the same on a file
// system that drops syncs (envDMLogLoose), which loses nothing without a loss of power.
func TestWorkloadPhases(t *testing.T) {
	for _, loose := range []string{"", "files", "dirs"} {
		t.Run("loose="+loose, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestDMLogWorkload$", "-test.count=1")
			cmd.Env = append(os.Environ(), envDMLogWorkload+"="+root, envDMLogLoose+"="+loose)
			cmd.Stderr = os.Stderr
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			p := newDMLogPoint()
			var output, phases, gc []string
			lines := bufio.NewScanner(stdout)
			for lines.Scan() {
				text, ok := strings.CutPrefix(lines.Text(), "dmlog: ")
				if !ok {
					output = append(output, lines.Text())
					if removed, ok := strings.CutPrefix(lines.Text(), "workload: the GC "); ok {
						gc = append(gc, removed)
					}
					continue
				}
				mark := strings.ReplaceAll(text, " ", "-")
				if err := p.see(dmlog.Entry{Flags: dmlog.FlagMark, Mark: mark}); err != nil {
					t.Error(err)
				}
				if !strings.HasPrefix(mark, "ack-") {
					phases = append(phases, mark)
				}
				if _, err := io.WriteString(stdin, "go\n"); err != nil {
					t.Fatalf("the reply to %q: %v", text, err)
				}
			}
			if err := lines.Err(); err != nil {
				t.Error(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("the workload: %v\n%s", err, strings.Join(output, "\n"))
			}
			if want := (dmlogPoint{acked: dmlogUpdates, closed: true, backup: 160, generation: 3}); *p != want {
				t.Errorf("the marks tell %+v, want %+v", *p, want)
			}
			// The reader and the writer after it see the Update 41, whose record the first process has written.
			if got, want := strings.Join(phases, " "), "seen-41 open seen-41 open seen-69 open seen-100 open seen-129 "+
				"backup-160 closed maintain maintain maintain generation-3 closed open seen-180 closed"; got != want {
				t.Errorf("the marks of the phases are %q, want %q", got, want)
			}
			wantGC := []string{
				`before the compaction has removed \{Segments:0 Objects:1 Temps:0\}`,
				`before the compaction has removed \{Segments:1 Objects:0 Temps:0\}`,
				`before the compaction has removed \{Segments:[1-9][0-9]* Objects:0 Temps:0\}`,
				`after the compaction has removed \{Segments:0 Objects:0 Temps:0\}`,
			}
			if !slices.EqualFunc(gc, wantGC, func(line, want string) bool {
				return regexp.MustCompile("^" + want + "$").MatchString(line)
			}) {
				t.Errorf("the GC of the maintenances has removed %q, want %q", gc, wantGC)
			}

			s, err := open(filepath.Join(root, filepath.FromSlash(workloadData)), workloadOptions())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := checkWorkload(t, s, dmlogUpdates, dmlogUpdates, true); err != nil {
				t.Error(err)
			}
			if g := logDB(t, s).Recovery().Generation; g != 3 {
				t.Errorf("the store is of the generation %d, want 3", g)
			}
			if err := s.Close(); err != nil {
				t.Error(err)
			}
			r := &dmlogReport{}
			if err := r.checkBackup(t, root, p); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestWorkloadInterrupted checks that the workload that gets SIGTERM, as record.sh sends when it stops, ends with the
// process of its phase, which waits for a reply: no process has the store open then.
func TestWorkloadInterrupted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDMLogWorkload$", "-test.count=1")
	cmd.Env = append(os.Environ(), envDMLogWorkload+"="+root, envDMLogLoose+"=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	// The process of the phase would end without a reply too: the replies stay open until the end of the test.
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	for lines.Scan() && lines.Text() != "dmlog: ack 5" {
		if strings.HasPrefix(lines.Text(), "dmlog: ") {
			if _, err := io.WriteString(stdin, "go\n"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if lines.Text() != "dmlog: ack 5" {
		t.Fatalf("the workload has ended before the Update 5: %v", lines.Err())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	select {
	case err := <-ended:
		if ee, ok := errors.AsType[*exec.ExitError](err); !ok || ee.ExitCode() != 1 {
			t.Errorf("the workload has ended with %v after SIGTERM, want that it fails by itself", err)
		}
	case <-time.After(30 * time.Second):
		// Without a reply, the process of the phase ends, and the workload with it.
		_ = stdin.Close()
		t.Fatalf("the workload has not ended in 30 s after SIGTERM; then it has ended with %v", <-ended)
	}
	// The writer lock is free.
	s, err := open(filepath.Join(root, filepath.FromSlash(workloadData)), workloadOptions())
	if err != nil {
		t.Fatalf("open() after the workload = %v", err)
	}
	if n, err := pairOf(t.Context(), s); err != nil || n != 5 {
		t.Errorf("the store has the pair of %d, %v; want 5", n, err)
	}
	if err := s.Close(); err != nil {
		t.Error(err)
	}
}
