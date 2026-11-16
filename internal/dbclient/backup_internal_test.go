package dbclient

import (
	"strings"
	"testing"
)

// TestCheckBackupPath checks the paths that Backup makes: dbwire refuses most of the others before they get here, and
// this check is what is left of the destination if it did not.
func TestCheckBackupPath(t *testing.T) {
	for _, p := range []string{"files", "logdb", "files/ab", "files/ab/cd", "logdb/lock/writer", "logdb/objects/ab/cd", "files/a.b", "files/.tmp"} {
		if err := checkBackupPath("copy", p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range []string{
		"", ".", "..", "/", "/files", "/etc/passwd", "//files", "files/", "files//ab", "files/./ab", "files/..", "files/../ab",
		"files/../../x", "../files", "./files", "files/ab/..", "../logdb", "logdb/../..", `files\ab`, `files/..\x`, "files:x", "C:/files",
		"files/a\x00b", "other", "other/x", "filesystem", "files2/x", "logdb2", "Files", "LOGDB", "x", "file", "log", " files",
		"files ", "x/files", "x/../files",
	} {
		err := checkBackupPath("copy", p)
		if err == nil {
			t.Errorf("%q is a path of a backup", p)
			continue
		}
		if want := "copy: the backup has an entry for "; !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v", p, err)
		}
	}
}
