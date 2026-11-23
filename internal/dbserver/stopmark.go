package dbserver

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The mark that a daemon leaves in the data directory when it stops after a sync failed: the page cache may hold changes
// that never reach the disk, so no daemon may open the directory again until the machine has restarted, which the boot ID
// of the machine tells.
const (
	markName = "stopped"
	bootFile = "/proc/sys/kernel/random/boot_id"
	maxMark  = 4096
	maxCause = 1024
)

// ErrNotRestarted is wrapped by the error of Open for a data directory whose mark of a failed sync has the boot ID of the
// machine that is running now.
var ErrNotRestarted = errors.New("the machine has not restarted since a sync failed")

type notRestartedError struct{ text string }

func (e *notRestartedError) Error() string { return e.text }
func (e *notRestartedError) Unwrap() error { return ErrNotRestarted }

// machineBoot returns the boot ID of the machine, which changes at each start of it.
func machineBoot() (string, error) {
	b, err := os.ReadFile(bootFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(b), "\n"), nil
}

// validBoot reports whether id is a boot ID in the form of the kernel: 36 bytes, lower case hex digits and hyphens.
func validBoot(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := range len(id) {
		switch c := id[i]; {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c < '0' || c > '9' && c < 'a' || c > 'f':
			return false
		}
	}
	return true
}

// bootOf returns the boot ID of the machine that o gives, or that of the kernel.
func bootOf(o Options) (string, error) {
	read := machineBoot
	if o.Boot != nil {
		read = o.Boot
	}
	id, err := read()
	switch {
	case err != nil:
		return "", fmt.Errorf("cannot read the boot ID of the machine: %w; whiteroom-db needs it to know whether the machine "+
			"has restarted since a sync failed", err)
	case !validBoot(id):
		return "", fmt.Errorf("the boot ID of the machine is not valid: %q", id)
	}
	return id, nil
}

// stopMark is what readMark has read of a mark. Its boot ID is valid; the time and the cause are what the lines had.
type stopMark struct {
	boot  string
	at    time.Time // zero if the mark has no time that parses
	cause string    // empty if the mark has none that can be shown
}

// markText is the text of the mark of a stop at at, whose cause was err: the text of safeText, in one line.
func markText(boot string, at time.Time, err error) []byte {
	cause := strings.ReplaceAll(safeText(err), "\n", "; ")
	if len(cause) > maxCause {
		cause = strings.ToValidUTF8(cause[:maxCause], "")
	}
	return fmt.Appendf(nil, "whiteroom-db stopped after a sync failed; restart the machine before starting it again\n"+
		"boot: %s\ntime: %s\ncause: %s\n", boot, at.UTC().Truncate(time.Second).Format(time.RFC3339), cause)
}

func printable(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// readMark reads the mark in the data directory root, which name names in errors. It returns nil if there is none, and an
// error for one that cannot be read: it is not a regular file, it is larger than maxMark bytes, or it has no boot line.
func readMark(root vfs.FS, name string) (*stopMark, error) {
	path := fspath.Join(name, markName)
	info, err := root.Lstat(markName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, readError(path, err)
	case !info.Mode().IsRegular() || info.Size() > maxMark:
		return nil, unreadableMark(path)
	}
	f, err := root.Open(markName)
	if err != nil {
		return nil, readError(path, err)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxMark+1))
	_ = f.Close()
	if err != nil {
		return nil, readError(path, err)
	}
	if len(b) > maxMark {
		return nil, unreadableMark(path)
	}
	m := &stopMark{}
	for line := range strings.SplitSeq(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "boot: "):
			if id := line[len("boot: "):]; m.boot == "" && validBoot(id) {
				m.boot = id
			}
		case strings.HasPrefix(line, "time: "):
			if t, err := time.Parse(time.RFC3339, line[len("time: "):]); err == nil && m.at.IsZero() {
				m.at = t.UTC()
			}
		case strings.HasPrefix(line, "cause: "):
			if c := line[len("cause: "):]; m.cause == "" && printable(c) {
				m.cause = c
			}
		}
	}
	if m.boot == "" {
		return nil, unreadableMark(path)
	}
	return m, nil
}

func unreadableMark(path string) error {
	return fmt.Errorf("%s: the mark of a failed sync has no boot ID that this program can read; restart the machine if it "+
		"has not restarted since the sync failed, and then remove the mark", path)
}

// what returns "a sync failed", with the time and the cause of the mark if it has them.
func (m *stopMark) what() string {
	text := "a sync failed"
	if !m.at.IsZero() {
		text += " at " + m.at.Format(time.RFC3339)
	}
	if m.cause != "" {
		text += " (" + m.cause + ")"
	}
	return text
}

// refusal is the error of a data directory whose mark is from the boot of the machine that runs now.
func (m *stopMark) refusal(name string) error {
	return &notRestartedError{name + ": " + m.what() + ", and the machine has not restarted since; restart the machine " +
		"before starting whiteroom-db on this directory again"}
}

// checkMark reads the mark, and returns the mark of an earlier boot, if there is one: a mark of the boot, and one that
// cannot be read, are errors.
func checkMark(root vfs.FS, name, boot string) (*stopMark, error) {
	m, err := readMark(root, name)
	switch {
	case err != nil:
		return nil, err
	case m != nil && m.boot == boot:
		return nil, m.refusal(name)
	}
	return m, nil
}

// removeMark removes the mark m of an earlier boot, which the machine has restarted since, without syncing the directory:
// a crash that brings it back brings back a mark of an earlier boot, which the next start removes again.
func removeMark(root vfs.FS, name string, m *stopMark, logf func(format string, args ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := root.Remove(markName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logf("cannot remove the mark %s: %s", fspath.Join(name, markName), innerText(err))
		return
	}
	logf("%s: %s before the machine restarted; the mark %s is removed: check the store with whiteroom doctor", name, m.what(),
		fspath.Join(name, markName))
}

// writeMark writes the mark of a stop at at, whose cause was cause, and makes it durable. It goes on after a failed sync of
// the file: the name may still reach the disk.
func writeMark(root vfs.FS, boot string, at time.Time, cause error) error {
	f, err := root.OpenFile(markName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	text := markText(boot, at, cause)
	n, err := f.Write(text)
	if err == nil && n < len(text) {
		err = io.ErrShortWrite
	}
	return errors.Join(err, f.Sync(), f.Close(), root.SyncDir("."))
}

// leaveText is the sentence for a mark that could not be written.
func leaveText(name string, err error) string {
	return fmt.Sprintf("cannot leave the mark %s (%s); do not start the daemon on %s again before the machine restarts",
		fspath.Join(name, markName), innerText(err), name)
}

// innerText is the text of err without the operation and the path of an *fs.PathError, so that it reads the same on the
// OS and on a simulated file system; the errors of an errors.Join are joined with "; ".
func innerText(err error) string {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var parts []string
		for _, e := range j.Unwrap() {
			parts = append(parts, innerText(e))
		}
		return strings.Join(parts, "; ")
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
