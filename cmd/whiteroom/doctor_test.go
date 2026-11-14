//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// noteLine starts the line that whiteroom doctor writes on stderr when it has not checked the published documents.
const noteLine = "whiteroom doctor: note: "

// doctorOn runs whiteroom doctor with -data data, the arguments args and the text stdin as the standard input, and checks
// that the data directory is as it was: no file is made, removed or changed in it.
func doctorOn(t *testing.T, data, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	before := stateOf(t, data)
	code, stdout, stderr = runCmdIn(t, strings.NewReader(stdin), append([]string{"doctor", "-data", data}, args...)...)
	if after := stateOf(t, data); after != before {
		t.Errorf("whiteroom doctor %s changed the data directory:\n%s\nwas:\n%s", strings.Join(args, " "), after, before)
	}
	return code, stdout, stderr
}

// doctorLines splits what whiteroom doctor printed on stdout into the lines of the problems and the last line.
func doctorLines(t *testing.T, stdout string) (problems []string, last string) {
	t.Helper()
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("whiteroom doctor printed %q, which is not lines", stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	return lines[:len(lines)-1], lines[len(lines)-1]
}

// checkStderr checks what whiteroom doctor has printed on stderr: nothing, or if note is true, the one line that tells that
// the published documents are not checked.
func checkStderr(t *testing.T, what, stderr string, note bool) {
	t.Helper()
	switch {
	case !note && stderr != "":
		t.Errorf("%s: whiteroom doctor printed on stderr %q, want nothing", what, stderr)
	case note && (!strings.HasPrefix(stderr, noteLine) || strings.Count(stderr, "\n") != 1):
		t.Errorf("%s: whiteroom doctor printed on stderr %q, want the one line of the note", what, stderr)
	}
}

// checkClean checks that whiteroom doctor of the data directory data has found no problem: the one line on stdout and the
// exit code 0.
func checkClean(t *testing.T, what, data string, code int, stdout, stderr string, note bool) {
	t.Helper()
	if want := "no problems found in " + data + "\n"; code != 0 || stdout != want {
		t.Errorf("%s: whiteroom doctor = %d, with stdout %q, want 0 and %q", what, code, stdout, want)
	}
	checkStderr(t, what, stderr, note)
}

// checkProblems checks that whiteroom doctor of the data directory data has found problems in the parts, one problem of
// each and in that order: the exit code is 1, the lines of stdout start with the parts, and the last line counts them. It
// returns the lines of the problems.
func checkProblems(t *testing.T, what, data string, code int, stdout, stderr string, note bool, parts ...string) []string {
	t.Helper()
	checkStderr(t, what, stderr, note)
	problems, last := doctorLines(t, stdout)
	want := count(len(parts), "problem") + " found in " + data
	if code != 1 || last != want || len(problems) != len(parts) {
		t.Errorf("%s: whiteroom doctor = %d, with stdout:\n%s\nwant 1, %d problems and the last line %q", what, code, stdout, len(parts), want)
		return problems
	}
	for i, part := range parts {
		if !strings.HasPrefix(problems[i], part+": ") {
			t.Errorf("%s: the problem %d is %q, want one of the part %s", what, i+1, problems[i], part)
		}
	}
	return problems
}

// entryIDs returns the IDs of the entries of the data directory, which no process writes, in order.
func entryIDs(t *testing.T, data string) []string {
	t.Helper()
	s, err := store.OpenReader(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var ids []string
	err = s.View(t.Context(), func(tx *store.Tx) error {
		docs, err := tx.Documents(content.RefEntry)
		for _, d := range docs {
			ids = append(ids, d.ID)
		}
		return err
	})
	if err != nil || len(ids) < 3 {
		t.Fatalf("the entries of the data directory of the sample site: %v, %v", ids, err)
	}
	return ids
}

// dropEntryRows deletes the rows of the index of the entries ids, as a writer of the data directory, which it closes.
func dropEntryRows(t *testing.T, data string, ids ...string) {
	t.Helper()
	s, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := store.DropIndexRow(t.Context(), s, content.RefEntry, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// originalFiles returns the files of the originals in the data directory, the files below files/ but those that a process
// is making (.tmp-), in order.
func originalFiles(t *testing.T, data string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(data, "files"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.HasPrefix(d.Name(), ".tmp-") {
			files = append(files, p)
		}
		return err
	})
	if err != nil || len(files) < 2 {
		t.Fatalf("the originals of the data directory of the sample site: %q, %v", files, err)
	}
	return files
}

// hashOf returns the hash of the original in the file, as the media documents have it.
func hashOf(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// flipBit changes the lowest bit of the byte at off in the file.
func flipBit(t *testing.T, file string, off int64) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	_, err = f.ReadAt(b, off)
	if err == nil {
		b[0] ^= 1
		_, err = f.WriteAt(b, off)
	}
	if err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// damageLog changes a byte in the middle of the log of the engine of the data directory.
func damageLog(t *testing.T, data string) {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(data, "logdb", "log", "*.log"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("the logs of the data directory: %q, %v", logs, err)
	}
	info, err := os.Stat(logs[len(logs)-1])
	if err != nil {
		t.Fatal(err)
	}
	flipBit(t, logs[len(logs)-1], info.Size()/2)
}

// damageSchema changes the schema file of the site directory site so that a revision of the sample site is not valid.
func damageSchema(t *testing.T, site string) {
	t.Helper()
	schema := filepath.Join(site, "site", "schema.yaml")
	data, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	strict := strings.Replace(string(data), "maxLength: 100", "maxLength: 1", 1)
	if strict == string(data) {
		t.Fatalf("%s has no maxLength: 100", schema)
	}
	if err := os.WriteFile(schema, []byte(strict), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorClean checks whiteroom doctor of data directories that have no problem: what whiteroom init has made, with and
// without SITE and -theme, one without settings, and the copy that whiteroom backup has taken, with the heads that it
// printed. It prints one line and exits with 0, and changes nothing in the data directory.
func TestDoctorClean(t *testing.T) {
	s := initSample(t)
	code, stdout, stderr := doctorOn(t, s.data, "")
	checkClean(t, "no SITE", s.data, code, stdout, stderr, true)
	code, stdout, stderr = doctorOn(t, s.data, "", s.site)
	checkClean(t, "SITE", s.data, code, stdout, stderr, false)
	code, stdout, stderr = doctorOn(t, s.data, "", "-theme", exampleTheme, s.site)
	checkClean(t, "SITE and -theme", s.data, code, stdout, stderr, false)
	// Only the schema file and the theme are read of SITE.
	only := initSample(t)
	schemaOnly(t, only.site, true)
	code, stdout, stderr = doctorOn(t, only.data, "", only.site)
	checkClean(t, "SITE with only the schema file and the theme", only.data, code, stdout, stderr, false)
	// The theme of -theme, when SITE has none.
	schemaOnly(t, only.site, false)
	code, stdout, stderr = doctorOn(t, only.data, "", "-theme", exampleTheme, only.site)
	checkClean(t, "SITE with only the schema file, and -theme", only.data, code, stdout, stderr, false)

	empty := initEmpty(t)
	code, stdout, stderr = doctorOn(t, empty.data, "")
	checkClean(t, "no settings and no SITE", empty.data, code, stdout, stderr, true)

	dest := filepath.Join(t.TempDir(), "backup")
	code, printed, stderr := runCmd(t, "backup", "-data", s.data, dest)
	if code != 0 || stderr != "" {
		t.Fatalf("whiteroom backup = %d, with stderr:\n%s", code, stderr)
	}
	code, stdout, stderr = doctorOn(t, dest, "", s.site)
	checkClean(t, "the copy", dest, code, stdout, stderr, false)
	heads := writeHeads(t, printed)
	code, stdout, stderr = doctorOn(t, dest, "", "-heads", heads, s.site)
	checkClean(t, "the copy, with the heads that the backup printed", dest, code, stdout, stderr, false)
	code, stdout, stderr = doctorOn(t, s.data, "", "-heads", heads, s.site)
	checkClean(t, "the data directory, with the heads that the backup printed", s.data, code, stdout, stderr, false)
}

// TestDoctorProblems damages data directories that whiteroom init has made, and checks the problems that whiteroom doctor
// tells: a row of the index that is missing; an original that is missing or has other bytes; a log of the engine that
// cannot be read; and the order of the parts when there are several. Each is a line of stdout, which starts with its part
// and names what is wrong, and the exit code is 1; nothing is changed in the data directory.
func TestDoctorProblems(t *testing.T) {
	t.Run("a row of the index", func(t *testing.T) {
		s := initSample(t)
		id := entryIDs(t, s.data)[0]
		dropEntryRows(t, s.data, id)
		code, stdout, stderr := doctorOn(t, s.data, "", s.site)
		problems := checkProblems(t, "a row of the index", s.data, code, stdout, stderr, false, "index")
		if len(problems) == 1 && !strings.Contains(problems[0], id) {
			t.Errorf("the problem %q does not name the entry %s", problems[0], id)
		}
	})

	t.Run("an original that is missing", func(t *testing.T) {
		s := initSample(t)
		file := originalFiles(t, s.data)[0]
		hash := hashOf(t, file)
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := doctorOn(t, s.data, "", s.site)
		problems := checkProblems(t, "an original that is missing", s.data, code, stdout, stderr, false, "original")
		if len(problems) == 1 && !strings.Contains(problems[0], hash) {
			t.Errorf("the problem %q does not name the original %s", problems[0], hash)
		}
	})

	t.Run("an original that has other bytes", func(t *testing.T) {
		s := initSample(t)
		file := originalFiles(t, s.data)[1]
		hash := hashOf(t, file)
		flipBit(t, file, 0)
		code, stdout, stderr := doctorOn(t, s.data, "", s.site)
		problems := checkProblems(t, "an original that has other bytes", s.data, code, stdout, stderr, false, "original")
		if len(problems) == 1 && !strings.Contains(problems[0], hash) {
			t.Errorf("the problem %q does not name the original %s", problems[0], hash)
		}
	})

	t.Run("a log that cannot be read", func(t *testing.T) {
		s := initSample(t)
		damageLog(t, s.data)
		code, stdout, stderr := doctorOn(t, s.data, "", s.site)
		checkProblems(t, "a log that cannot be read", s.data, code, stdout, stderr, false, "open")
		// Nothing else is checked, what -heads says included.
		heads := writeHeads(t, "audit head activity seven sha256:zz\n")
		code, stdout, stderr = doctorOn(t, s.data, "", "-heads", heads, s.site)
		checkProblems(t, "a log that cannot be read, and a line of heads that cannot", s.data, code, stdout, stderr, false, "open")
	})

	t.Run("several parts", func(t *testing.T) {
		s := initSample(t)
		ids := entryIDs(t, s.data)
		file := originalFiles(t, s.data)[0]
		hash := hashOf(t, file)
		dropEntryRows(t, s.data, ids[0], ids[1])
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := doctorOn(t, s.data, "", s.site)
		problems := checkProblems(t, "several parts", s.data, code, stdout, stderr, false, "index", "index", "original")
		if len(problems) == 3 {
			for i, id := range ids[:2] {
				if !strings.Contains(problems[i], id) {
					t.Errorf("the problem %q does not name the entry %s", problems[i], id)
				}
			}
			if !strings.Contains(problems[2], hash) {
				t.Errorf("the problem %q does not name the original %s", problems[2], hash)
			}
		}
		// The same problems without SITE, and the note.
		code, stdout, stderr = doctorOn(t, s.data, "")
		checkProblems(t, "several parts without SITE", s.data, code, stdout, stderr, true, "index", "index", "original")
	})
}

// splitLines returns the lines of text, each with its line break.
func splitLines(text string) []string { return slices.Collect(strings.Lines(text)) }

// writeHeads writes text as a file in a new directory, and returns its path.
func writeHeads(t *testing.T, text string) string {
	t.Helper()
	return writeConfig(t, t.TempDir(), "heads.txt", text)
}

// servedLog runs a whiteroom serve admin on the data directory of s until a login and the first build have been done, and
// the function more, if there is one, has used the server; it stops the server and returns what it has written on stderr.
func servedLog(t *testing.T, s adminSite, more func(a *adminProc, cookie string)) string {
	t.Helper()
	a := startAdmin(t, t.Context(), s.args()...)
	cookie := a.login(t)
	if built := a.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the start build has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	if more != nil {
		more(a, cookie)
	}
	if code := a.stop(t); code != 0 {
		t.Fatalf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
	return a.stderr.String()
}

// TestDoctorHeads gives whiteroom doctor the heads of the audit logs that a server has written, as they are and with the
// lines of a logger around them: the data directory is right. A head that is not the hash of its record, and a head of a
// record that is not there, are problems of the part audit; a line that has "audit head " and no head, and a file without
// a head, are problems of the part heads, which have nothing of the line; the standard input can be the file; and the
// heads that a server writes after a backup are not those of the copy.
func TestDoctorHeads(t *testing.T) {
	s := initSample(t)
	served := servedLog(t, s, nil)
	// What a logger puts before a line, and lines that are no heads.
	var text strings.Builder
	text.WriteString("2026-11-14T10:00:00Z whiteroom: started\n")
	for _, line := range splitLines(served) {
		if strings.HasPrefix(line, headPrefix) {
			line = "Nov 14 10:00:01 host whiteroom[1]: " + line
		}
		text.WriteString(line)
	}
	text.WriteString("2026-11-14T10:00:02Z whiteroom: stopped\n")
	good := text.String()
	lines := splitLines(good)
	var headAt []int // the numbers of the lines that have a head
	for i, line := range lines {
		if strings.Contains(line, headPrefix) {
			headAt = append(headAt, i)
		}
	}
	if len(headAt) < 2 {
		t.Fatalf("the server wrote %d lines of heads, want one for each log that has records:\n%s", len(headAt), served)
	}
	changed := func(at int, edit func(line string) string) string {
		out := slices.Clone(lines)
		out[at] = edit(out[at])
		return strings.Join(out, "")
	}
	// A hash with another last digit: the line can still be read.
	otherHash := func(line string) string {
		line = strings.TrimSuffix(line, "\n")
		digit := "0"
		if strings.HasSuffix(line, "0") {
			digit = "1"
		}
		return line[:len(line)-1] + digit + "\n"
	}

	code, stdout, stderr := doctorOn(t, s.data, "", "-heads", writeHeads(t, good), s.site)
	checkClean(t, "the heads as the server wrote them", s.data, code, stdout, stderr, false)
	code, stdout, stderr = doctorOn(t, s.data, "", "-heads", writeHeads(t, served))
	checkClean(t, "the stderr of the server as it is, without SITE", s.data, code, stdout, stderr, true)
	code, stdout, stderr = doctorOn(t, s.data, good, "-heads", "-", s.site)
	checkClean(t, "the heads on the standard input", s.data, code, stdout, stderr, false)
	code, stdout, stderr = doctorOn(t, s.data, "", "-heads", writeHeads(t, good+good), s.site)
	checkClean(t, "the heads twice", s.data, code, stdout, stderr, false)

	t.Run("a head that is not the hash of its record", func(t *testing.T) {
		text := changed(headAt[len(headAt)-1], otherHash)
		code, stdout, stderr := doctorOn(t, s.data, "", "-heads", writeHeads(t, text), s.site)
		problems := checkProblems(t, "a changed hash", s.data, code, stdout, stderr, false, "audit")
		if len(problems) == 1 && !strings.Contains(problems[0], "head kept outside the store") {
			t.Errorf("the problem %q does not tell that the head kept outside the store is not the hash", problems[0])
		}
	})

	t.Run("a problem of the audit logs, and a line that cannot be read", func(t *testing.T) {
		text := changed(headAt[len(headAt)-1], otherHash) + "audit head activity seven\n"
		code, stdout, stderr := doctorOn(t, s.data, "", "-heads", writeHeads(t, text), s.site)
		checkProblems(t, "an audit problem and a head that cannot be read", s.data, code, stdout, stderr, false, "audit", "heads")
	})

	t.Run("a head of a record that is not there", func(t *testing.T) {
		text := changed(headAt[0], func(line string) string {
			_, rest, _ := strings.Cut(line, headPrefix)
			h, err := audit.ParseHead(headPrefix + rest)
			if err != nil {
				t.Fatal(err)
			}
			h.N += 1000
			return h.String() + "\n"
		})
		code, stdout, stderr := doctorOn(t, s.data, "", "-heads", writeHeads(t, text), s.site)
		problems := checkProblems(t, "a head after the end of the log", s.data, code, stdout, stderr, false, "audit")
		if len(problems) == 1 && !strings.Contains(problems[0], "ends with record") {
			t.Errorf("the problem %q does not tell where the log ends", problems[0])
		}
	})

	t.Run("a line with a head that cannot be read", func(t *testing.T) {
		const secret = "token-4f9d2c71"
		bad := "2026-11-14T10:00:03Z whiteroom: audit head activity seven sha256:zz " + secret + "\n"
		file := writeHeads(t, good+bad)
		code, stdout, stderr := doctorOn(t, s.data, "", "-heads", file, s.site)
		problems := checkProblems(t, "a line that cannot be read", s.data, code, stdout, stderr, false, "heads")
		want := fmt.Sprintf("heads: %s:%d: ", file, len(lines)+1)
		if len(problems) == 1 && !strings.HasPrefix(problems[0], want) {
			t.Errorf("the problem %q does not start with %q", problems[0], want)
		}
		if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
			t.Errorf("whiteroom doctor printed what the line has: %q, %q", stdout, stderr)
		}
		// Two of them, with a line that has no "audit head " between them, which is skipped.
		file = writeHeads(t, bad+"audit hea\n"+bad)
		code, stdout, stderr = doctorOn(t, s.data, "", "-heads", file, s.site)
		problems = checkProblems(t, "two lines that cannot be read", s.data, code, stdout, stderr, false, "heads", "heads")
		if len(problems) == 2 && (!strings.HasPrefix(problems[0], "heads: "+file+":1: ") || !strings.HasPrefix(problems[1], "heads: "+file+":3: ")) {
			t.Errorf("the problems %q do not tell the lines 1 and 3", problems)
		}
	})

	t.Run("no head", func(t *testing.T) {
		for _, tt := range []struct{ what, text string }{
			{"an empty file", ""},
			{"lines without the marker", "one\ntwo audit hea\n"},
			{"a line with a marker that is too long to be read", "audit head " + strings.Repeat("x", 3<<20) + "\n"},
		} {
			code, stdout, stderr := doctorOn(t, s.data, "", "-heads", writeHeads(t, tt.text), s.site)
			problems := checkProblems(t, tt.what, s.data, code, stdout, stderr, false, "heads")
			if len(problems) == 1 && strings.Contains(problems[0], "xxxx") {
				t.Errorf("%s: the problem has the line: %.200q", tt.what, problems[0])
			}
		}
	})

	t.Run("a line that is too long", func(t *testing.T) {
		// Skipped, but counted: the line that follows it is the second after the heads.
		long := "x audit head " + strings.Repeat("y", 3<<20) + "\n"
		file := writeHeads(t, good+long+"z audit head x\n")
		code, stdout, stderr := doctorOn(t, s.data, "", "-heads", file, s.site)
		problems := checkProblems(t, "a line that is too long", s.data, code, stdout, stderr, false, "heads")
		want := fmt.Sprintf("heads: %s:%d: ", file, len(lines)+2)
		if len(problems) == 1 && !strings.HasPrefix(problems[0], want) {
			t.Errorf("the problem %q does not start with %q", problems[0], want)
		}
	})

	t.Run("after a backup", func(t *testing.T) {
		s := initSample(t)
		dest := filepath.Join(t.TempDir(), "backup")
		code, printed, stderr := runCmd(t, "backup", "-data", s.data, dest)
		if code != 0 || stderr != "" {
			t.Fatalf("whiteroom backup = %d, with stderr:\n%s", code, stderr)
		}
		h1 := writeHeads(t, printed)
		h2 := writeHeads(t, servedLog(t, s, func(a *adminProc, cookie string) {
			id := a.publishPage(t, cookie, "en", "After the backup", "after-the-backup")
			a.waitPublished(t, cookie, id)
		}))
		for _, tt := range []struct {
			what, data, heads string
			clean             bool
		}{
			{"the copy, with its heads", dest, h1, true},
			{"the copy, with the heads written after it", dest, h2, false},
			{"the data directory, with the heads before the writes", s.data, h1, true},
			{"the data directory, with the heads after them", s.data, h2, true},
		} {
			code, stdout, stderr := doctorOn(t, tt.data, "", "-heads", tt.heads, s.site)
			if tt.clean {
				checkClean(t, tt.what, tt.data, code, stdout, stderr, false)
			} else {
				checkProblems(t, tt.what, tt.data, code, stdout, stderr, false, "audit")
			}
		}
	})
}

// TestDoctorSite checks the part site: what is published, which the schema and the theme of SITE do not accept, is a
// problem for each of what a build tells, with the names of the documents of the data directory and the path of the
// schema file as a build tells them; without SITE nothing of it is checked; a data directory without settings, and a
// schema that is not YAML, are problems too.
func TestDoctorSite(t *testing.T) {
	t.Run("a schema that does not accept a document", func(t *testing.T) {
		s := initSample(t)
		damageSchema(t, s.site)
		code, stdout, stderr := doctorOn(t, s.data, "", s.site)
		problems, last := doctorLines(t, stdout)
		if code != 1 || stderr != "" || len(problems) == 0 || last != count(len(problems), "problem")+" found in "+s.data {
			t.Fatalf("whiteroom doctor = %d, with stdout:\n%s\nand stderr %q, want 1, the problems of the site, and their number", code, stdout,
				stderr)
		}
		found := false
		for _, line := range problems {
			if !strings.HasPrefix(line, "site: ") || strings.Contains(line, s.site) {
				t.Errorf("the problem %q is not one of the site, or has the site directory, which has no document", line)
			}
			found = found || strings.HasPrefix(line, "site: revisions/rev_01JC0000000000000000000002.json:") &&
				strings.Contains(line, "/fields/client: too long")
		}
		if !found {
			t.Errorf("whiteroom doctor printed:\n%s\nwant a line of revisions/rev_01JC0000000000000000000002.json about /fields/client", stdout)
		}

		// Without SITE, the documents are not checked, and the note says so.
		code, stdout, stderr = doctorOn(t, s.data, "")
		checkClean(t, "no SITE", s.data, code, stdout, stderr, true)
		// With -theme, the same.
		code, stdout, stderr = doctorOn(t, s.data, "", "-theme", exampleTheme, s.site)
		if again, _ := doctorLines(t, stdout); code != 1 || stderr != "" || !slices.Equal(again, problems) {
			t.Errorf("whiteroom doctor -theme = %d, with stdout:\n%s\nand stderr %q, want the problems as without -theme", code, stdout, stderr)
		}
	})

	t.Run("no settings", func(t *testing.T) {
		s := initEmpty(t)
		code, stdout, stderr := doctorOn(t, s.data, "", "-theme", s.theme, s.site)
		problems := checkProblems(t, "a data directory without settings", s.data, code, stdout, stderr, false, "site")
		if len(problems) == 1 && !strings.Contains(problems[0], "no site settings") {
			t.Errorf("the problem %q does not tell that the data directory has no settings", problems[0])
		}
	})

	t.Run("a schema that is not YAML", func(t *testing.T) {
		s := initSample(t)
		schema := filepath.Join(s.site, "site", "schema.yaml")
		if err := os.WriteFile(schema, []byte("a: [\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := doctorOn(t, s.data, "", s.site)
		problems := checkProblems(t, "a schema that is not YAML", s.data, code, stdout, stderr, false, "site")
		if len(problems) == 1 && !strings.HasPrefix(problems[0], "site: "+schema+":") {
			t.Errorf("the problem %q does not start with the path of the schema file %s", problems[0], schema)
		}
	})
}

// TestDoctorConfig checks the items of a config file: the paths are from its directory; the item data gives the data
// directory, and the flag -data wins; the item site gives SITE, which the argument wins over, and with it the published
// documents are checked; the item theme is used with a SITE only; a problem in the file is a usage error with no output.
func TestDoctorConfig(t *testing.T) {
	t.Run("data alone", func(t *testing.T) {
		root := storedRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml", "# the settings of every command\ndata: ../data\n"+
			"addr: 127.0.0.1:8081\nworkers: 2\n")
		code, stdout, stderr := runCmd(t, "doctor", "-config", file)
		checkClean(t, "the item data", filepath.Join(root, "data"), code, stdout, stderr, true)
	})

	t.Run("data, site and theme", func(t *testing.T) {
		root := storedRoot(t)
		conf, data := filepath.Join(root, "conf"), filepath.Join(root, "data")
		file := writeConfig(t, conf, "whiteroom.yaml", "data: ../data\nsite: ../site\ntheme: ../theme\n")
		code, stdout, stderr := runCmd(t, "doctor", "-config", file)
		checkClean(t, "the items", data, code, stdout, stderr, false)

		// The argument SITE wins over the item, which names a directory that is none.
		bad := writeConfig(t, conf, "bad-site.yaml", "data: ../data\nsite: ../no-such-site\ntheme: ../theme\n")
		code, stdout, stderr = runCmd(t, "doctor", "-config", bad)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "no-such-site") {
			t.Errorf("whiteroom doctor -config with a SITE that is none = %d, with stdout %q and stderr %q, want 1 and its path", code, stdout, stderr)
		}
		code, stdout, stderr = runCmd(t, "doctor", "-config", bad, filepath.Join(root, "site"))
		checkClean(t, "the argument SITE over the item", data, code, stdout, stderr, false)

		// The site of the file is checked: a schema that does not accept a document is a problem.
		damageSchema(t, filepath.Join(root, "site"))
		code, stdout, stderr = runCmd(t, "doctor", "-config", file)
		problems, _ := doctorLines(t, stdout)
		if code != 1 || stderr != "" || len(problems) == 0 || !strings.HasPrefix(problems[0], "site: ") {
			t.Errorf("whiteroom doctor -config with the site that has a problem = %d, with stdout:\n%s\nand stderr %q", code, stdout, stderr)
		}
	})

	t.Run("the flag wins over the item", func(t *testing.T) {
		root := storedRoot(t)
		bad := writeConfig(t, filepath.Join(root, "conf"), "bad.yaml", "data: ../no-such-data\n")
		data := filepath.Join(root, "data")
		code, stdout, stderr := runCmd(t, "doctor", "-config", bad, "-data", data)
		checkClean(t, "-data over the item", data, code, stdout, stderr, true)
		code, stdout, stderr = runCmd(t, "doctor", "-config", bad)
		checkProblems(t, "the item data of a directory that is none", filepath.Join(root, "no-such-data"), code, stdout, stderr, false, "open")
	})

	t.Run("a theme without a SITE", func(t *testing.T) {
		root := storedRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml", "data: ../data\ntheme: ../no-such-theme\n")
		code, stdout, stderr := runCmd(t, "doctor", "-config", file)
		checkClean(t, "the item theme without a SITE", filepath.Join(root, "data"), code, stdout, stderr, true)
		code, stdout, stderr = runCmd(t, "doctor", "-config", file, "-theme", filepath.Join(root, "theme"))
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom doctor: -theme is for SITE: ") {
			t.Errorf("whiteroom doctor -theme without a SITE = %d, with stdout %q and stderr %q, want 2 and the usage error", code, stdout, stderr)
		}
	})

	t.Run("a problem in the file", func(t *testing.T) {
		root := storedRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "unknown.yaml", "data: ../data\nsnack: 1\n")
		code, stdout, stderr := runCmd(t, "doctor", "-config", file)
		if want := file + ":2:1: /snack: unknown item\n"; code != 2 || stdout != "" || stderr != want {
			t.Errorf("whiteroom doctor -config = %d, with stdout %q and stderr %q, want 2, nothing and %q", code, stdout, stderr, want)
		}
	})
}

// TestDoctorBesideAdmin runs whiteroom doctor on the data directory that a whiteroom serve admin has open and writes: the
// server goes on, and the data directory has no problem, also after the server has published a page.
func TestDoctorBesideAdmin(t *testing.T) {
	s := initSample(t)
	a := startAdmin(t, t.Context(), s.args()...)
	cookie := a.login(t)
	if built := a.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the start build has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	id := a.publishPage(t, cookie, "en", "Published beside the doctor", "beside-the-doctor")
	if built := a.waitPublished(t, cookie, id); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the build of the publishing has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	for range 2 {
		code, stdout, stderr := runCmd(t, "doctor", "-data", s.data, s.site)
		checkClean(t, "beside the server", s.data, code, stdout, stderr, false)
		if status, _ := a.exchange(t, http.MethodGet, "/api/entries/"+id, "", "Cookie", cookie); status != http.StatusOK {
			t.Errorf("the server answers %d after whiteroom doctor, want %d", status, http.StatusOK)
		}
	}
	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
	code, stdout, stderr := doctorOn(t, s.data, "", s.site)
	checkClean(t, "after the server", s.data, code, stdout, stderr, false)
}

// TestDoctorErrors checks what whiteroom doctor cannot check: a file of -heads or of SITE that cannot be read is told on
// stderr, before the data directory is opened, with nothing on stdout; a data directory that cannot be opened is the
// problem open; and a context that is done is "interrupted".
func TestDoctorErrors(t *testing.T) {
	s := initSample(t)
	missing := filepath.Join(t.TempDir(), "missing")
	dir := t.TempDir()

	// An error of the command is told on stderr, and nothing is on stdout.
	failing := func(what, data, wantStderr string, args ...string) {
		t.Helper()
		code, stdout, stderr := doctorOn(t, data, "", args...)
		if code != 1 || stdout != "" || stderr != wantStderr {
			t.Errorf("%s: whiteroom doctor %s = %d, with stdout %q and stderr %q, want 1, nothing and %q", what,
				strings.Join(args, " "), code, stdout, stderr, wantStderr)
		}
	}
	failing("a file of -heads that is none", s.data, "whiteroom doctor: open "+missing+": no such file or directory\n",
		"-heads", missing, s.site)
	failing("a directory as the file of -heads", s.data, "whiteroom doctor: reading "+dir+": read "+dir+": is a directory\n",
		"-heads", dir, s.site)
	// Before the data directory is opened.
	failing("a file of -heads that is none, and a data directory that is none", missing,
		"whiteroom doctor: open "+missing+": no such file or directory\n", "-heads", missing, s.site)

	noSchema := initSample(t)
	if err := os.Remove(filepath.Join(noSchema.site, "site", "schema.yaml")); err != nil {
		t.Fatal(err)
	}
	failing("a SITE without the schema file", noSchema.data, filepath.Join(noSchema.site, "site", "schema.yaml")+": missing file\n",
		noSchema.site)
	failing("a SITE without the schema file, and a data directory that is none", missing,
		filepath.Join(noSchema.site, "site", "schema.yaml")+": missing file\n", noSchema.site)
	noTheme := initSample(t)
	schemaOnly(t, noTheme.site, false)
	failing("a SITE without a theme", noTheme.data, filepath.Join(noTheme.site, "theme")+": missing directory\n", noTheme.site)
	failing("a theme that is none", s.data, "whiteroom doctor: open "+missing+": no such file or directory\n", "-theme", missing, s.site)

	t.Run("a data directory that cannot be opened", func(t *testing.T) {
		for _, tt := range []struct{ what, data string }{{"a missing directory", missing}, {"an empty directory", t.TempDir()}} {
			code, stdout, stderr := doctorOn(t, tt.data, "", s.site)
			checkProblems(t, tt.what, tt.data, code, stdout, stderr, false, "open")
			// The files of SITE are checked first, and the site is not checked at all.
			code, stdout, stderr = doctorOn(t, tt.data, "")
			checkProblems(t, tt.what+", no SITE", tt.data, code, stdout, stderr, false, "open")
		}
	})

	t.Run("a context that is done", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		for _, args := range [][]string{{"doctor", "-data", s.data, s.site}, {"doctor", "-data", s.data}} {
			var stdout, stderr bytes.Buffer
			code := run(ctx, args, testEnv(strings.NewReader(""), &stdout, &stderr))
			if want := "whiteroom doctor: interrupted\n"; code != 1 || stdout.Len() > 0 || stderr.String() != want {
				t.Errorf("whiteroom %s = %d, with stdout %q and stderr %q, want 1, nothing and %q", strings.Join(args, " "), code,
					stdout.String(), stderr.String(), want)
			}
		}
	})
}

// TestReportLimit checks the report: a limit of problems, after which it takes no more and tells that it stopped; and the
// command that is given a limit, which stops the checks at it.
func TestReportLimit(t *testing.T) {
	var out bytes.Buffer
	r := &report{w: &out, limit: 2}
	if !r.add("a: one") || r.add("b: two\nlines") || r.add("c: three") {
		t.Errorf("add returned a value that is not true, false and false")
	}
	if code := r.finish("DATA"); code != 1 || out.String() != "a: one\nb: two lines\nstopped after 2 problems in DATA\n" {
		t.Errorf("a report with the limit 2 and 3 problems = %d, with %q", code, out.String())
	}

	s := initSample(t)
	ids := entryIDs(t, s.data)
	dropEntryRows(t, s.data, ids[0], ids[1], ids[2])
	for _, tt := range []struct {
		limit int
		want  string
		code  int
	}{
		{1, "stopped after 1 problem in " + s.data, 1},
		{2, "stopped after 2 problems in " + s.data, 1},
		{3, "stopped after 3 problems in " + s.data, 1},
		{4, "3 problems found in " + s.data, 1},
	} {
		var stdout, stderr bytes.Buffer
		code := doctor(t.Context(), []string{"-data", s.data, s.site}, testEnv(strings.NewReader(""), &stdout, &stderr), tt.limit)
		problems, last := doctorLines(t, stdout.String())
		if code != tt.code || last != tt.want || len(problems) != min(tt.limit, 3) || stderr.Len() > 0 {
			t.Errorf("whiteroom doctor with the limit %d = %d, with stdout:\n%s\nand stderr %q, want %d problems and %q", tt.limit, code,
				stdout.String(), stderr.String(), min(tt.limit, 3), tt.want)
		}
	}
}

// TestDoctorURL checks the published documents with -url and with the item url of a config file: the redirect that leads back
// to itself under the URL, which a server without it has saved, is a problem of the part site; it is none without the URL,
// under the URL of the settings, and under another URL. The flag wins over the item. Without SITE, -url is a usage error and
// the item is not used, and not looked at. A URL that does not pass is a usage error that prints nothing on stdout and does
// not open the data directory.
func TestDoctorURL(t *testing.T) {
	const (
		staging = "https://staging.example.com"
		other   = "https://other.example.com"
	)
	s := initSample(t)
	a := startAdmin(t, t.Context(), s.args()...)
	cookie := a.login(t)
	a.waitBuilt(t, cookie)
	if status, answer := a.createRedirect(t, cookie, selfRedirect); status != http.StatusCreated {
		t.Fatalf("POST /api/redirects = %d, with the body %s, want 201", status, answer)
	}
	stopsWith(t, "the server", a)

	// itself checks that whiteroom doctor has found the redirect that leads back to itself, and nothing else.
	itself := func(what string, code int, stdout, stderr string) {
		t.Helper()
		problems := checkProblems(t, what, s.data, code, stdout, stderr, false, "site")
		if len(problems) == 1 && (!strings.HasPrefix(problems[0], "site: redirects/rdr_") ||
			!strings.HasSuffix(problems[0], ": /to: redirects to itself")) {
			t.Errorf("%s: the problem is %q, want the redirect that leads back to itself", what, problems[0])
		}
	}
	clean := func(what string, code int, stdout, stderr string) {
		t.Helper()
		checkClean(t, what, s.data, code, stdout, stderr, false)
	}

	code, stdout, stderr := doctorOn(t, s.data, "", s.site)
	clean("no URL", code, stdout, stderr)
	code, stdout, stderr = doctorOn(t, s.data, "", "-url", staging, s.site)
	itself("-url", code, stdout, stderr)
	code, stdout, stderr = doctorOn(t, s.data, "", "-url", other, s.site)
	clean("-url of another site", code, stdout, stderr)
	code, stdout, stderr = doctorOn(t, s.data, "", "-url", "https://example.com", s.site)
	clean("-url of the URL of the settings", code, stdout, stderr)

	t.Run("the item of the config file", func(t *testing.T) {
		code, stdout, stderr := runCmd(t, "doctor", "-config", s.config(t, "url: "+staging))
		itself("the item", code, stdout, stderr)
		code, stdout, stderr = runCmd(t, "doctor", "-config", s.config(t, "url: "+other))
		clean("the item of another site", code, stdout, stderr)
		code, stdout, stderr = runCmd(t, "doctor", "-config", s.config(t, "url: "+other), "-url", staging)
		itself("-url over the item", code, stdout, stderr)
		code, stdout, stderr = runCmd(t, "doctor", "-config", s.config(t, "url: "+staging), "-url", "https://example.com")
		clean("-url over the item that leads back to itself", code, stdout, stderr)
		code, stdout, stderr = runCmd(t, "doctor", "-config", s.config(t, "url: "+staging), "-url", "")
		itself("-url of no text over the item", code, stdout, stderr)
		// An item that does not pass is not looked at if the flag gives a URL.
		code, stdout, stderr = runCmd(t, "doctor", "-config", s.config(t, "url: https://example.com/"), "-url", staging)
		itself("-url over an item that does not pass", code, stdout, stderr)
	})

	t.Run("no SITE", func(t *testing.T) {
		wantErr := "whiteroom doctor: -url is for SITE: give the directory SITE of the site to check, or site in the config file\n" +
			doctorUsageText(t)
		code, stdout, stderr := doctorOn(t, s.data, "", "-url", staging)
		if code != 2 || stdout != "" || stderr != wantErr {
			t.Errorf("whiteroom doctor -url without a SITE = %d, with stdout %q and stderr:\n%s\nwant 2, with no stdout and:\n%s", code,
				stdout, stderr, wantErr)
		}
		// The item is used with a SITE only, and is not looked at without it, whatever it is.
		for _, value := range []string{staging, "https://example.com/"} {
			file := writeConfig(t, t.TempDir(), "whiteroom.yaml", fmt.Sprintf("data: %q\nurl: %q\n", s.data, value))
			code, stdout, stderr := runCmd(t, "doctor", "-config", file)
			checkClean(t, "the item "+value+" without a SITE", s.data, code, stdout, stderr, true)
		}
	})

	t.Run("a URL that does not pass", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		for _, tt := range badSiteURLs[:3] {
			for _, fromItem := range []bool{false, true} {
				what, args := "-url", []string{"-url", tt.value}
				if fromItem {
					what, args = "url of the config file", []string{"-config", s.config(t, "url: "+strconv.Quote(tt.value))}
				}
				want := "whiteroom doctor: " + what + " is not a valid site URL: " + tt.reason + "\n"
				// Also for a data directory that is missing, which it does not come to open.
				for _, data := range []string{s.data, missing} {
					code, stdout, stderr := doctorOn(t, data, "", append(args, s.site)...)
					if code != 2 || stdout != "" || stderr != want {
						t.Errorf("%s of %q: whiteroom doctor = %d, with stdout %q and stderr:\n%s\nwant 2, with no stdout and:\n%s", what,
							tt.value, code, stdout, stderr, want)
					}
				}
			}
		}
	})
}

// doctorUsageText returns the usage that whiteroom doctor -h prints, which the usage errors print too.
func doctorUsageText(t *testing.T) string {
	t.Helper()
	code, stdout, help := runCmd(t, "doctor", "-h")
	if code != 0 || stdout != "" || !strings.HasPrefix(help, doctorUsage) {
		t.Fatalf("whiteroom doctor -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", code, stdout, help)
	}
	return help
}
