//go:build linux || darwin

package sitedir

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestNamedPipes checks that named pipes, which opening would wait on, are reported without being opened.
func TestNamedPipes(t *testing.T) {
	hash, digits := hashOf(original)
	dir := writeTree(t, map[string]string{"site/schema.yaml": "schemaVersion: 1\n"})
	for _, name := range []string{
		"site/settings.json", "entries/" + entryID + ".json", "revisions/" + revisionID + ".json", "files/" + digits,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(p, 0o644); err != nil {
			t.Skipf("cannot make named pipes: %v", err)
		}
	}
	d := openDir(t, dir)
	_, _, err := d.Settings()
	checkErrors(t, "Settings()", err, []string{"site/settings.json: must be a regular file"})
	_, err = d.Documents(content.RefEntry)
	checkErrors(t, "Documents()", err, []string{"entries/" + entryID + ".json: must be a regular file"})
	_, err = d.Revision(revisionID)
	checkErrors(t, "Revision()", err, []string{"revisions/" + revisionID + ".json: must be a regular file"})
	_, err = d.OpenMedia(hash)
	checkErrors(t, "OpenMedia()", err, []string{"files/" + digits + ": must be a regular file"})
}
