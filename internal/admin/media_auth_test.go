package admin

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// pipedRequest is a request whose body the test sends as slowly as it likes, to the Server directly.
type pipedRequest struct {
	body   *io.PipeWriter
	result <-chan *http.Response
}

// startPiped starts a request of method to path as the account login, with a body that is sent through a pipe, and sends the
// first part of it. It returns when the Server has read that part, so the request is past the checks before its body.
func (a *apiServer) startPiped(t *testing.T, login, method, path, contentType string, first []byte) pipedRequest {
	t.Helper()
	pr, pw := io.Pipe()
	result := async(func() *http.Response {
		return a.respond(a.bodyRequest(login, method, path, pr, "Content-Type", contentType))
	})
	t.Cleanup(func() { _ = pw.Close() })
	written := async(func() struct{} { _, _ = pw.Write(first); return struct{}{} })
	receive(t, written, "the first part of the body to be read")
	return pipedRequest{body: pw, result: result}
}

// finish sends the rest of the body, which ends it.
func (p pipedRequest) finish(rest []byte) {
	_, _ = p.body.Write(rest)
	_ = p.body.Close()
}

// wantRefusedUnread sends a request of the account login whose body must not be read, and checks that it is refused with 403,
// that the refusal is recorded once as access.denied for the permission and the target, by the account, and that the store is as
// it was. The clock moves a minute first, so that the limit of the records of refusals does not count.
func (a *apiServer) wantRefusedUnread(t *testing.T, login, method, path string, permission account.Action, target audit.Target) {
	t.Helper()
	a.clock.advance(time.Minute)
	before, n := a.versions(t), len(a.records(t, audit.AccessDenied))
	body := &unread{}
	checkProblem(t, method+" "+path+" as "+login, a.respond(a.bodyRequest(login, method, path, body, "Content-Type", "image/png")), "forbidden")
	if body.touched.Load() {
		t.Errorf("%s %s as %s: the body was read before the refusal", method, path, login)
	}
	records := a.records(t, audit.AccessDenied)
	if len(records) != n+1 {
		t.Errorf("%s %s as %s made %d records of access.denied, want 1", method, path, login, len(records)-n)
		return
	}
	if r := records[n]; r.Actor != a.people[login].account.ID || r.Action != audit.AccessDenied || r.Target != target ||
		len(r.Detail) != 1 || r.Detail["permission"] != string(permission) || r.Log != audit.ActivityLog {
		t.Errorf("%s %s as %s: the record is %+v, want the permission %s and the target %+v", method, path, login, r, permission, target)
	}
	if got := a.versions(t); got != before {
		t.Errorf("%s %s as %s has changed the store:\n%s\nwant\n%s", method, path, login, got, before)
	}
}

// TestMediaViewer shows that a viewer reads the files and uploads none: its upload and its replacement are refused before their
// bodies are read, whatever else is wrong with them, and each refusal is recorded.
func TestMediaViewer(t *testing.T) {
	a := newAPIServer(t)
	replace := "/api/media/" + sampleLogo + "/file"
	target := audit.Target{Kind: "media", ID: sampleLogo}
	a.wantRefusedUnread(t, "viewer", http.MethodPost, "/api/media?fileName=a.png", account.AddMedia, audit.Target{})
	a.wantRefusedUnread(t, "viewer", http.MethodPost, "/api/media", account.AddMedia, audit.Target{})
	a.wantRefusedUnread(t, "viewer", http.MethodPut, replace+"?base=1", account.EditMedia, target)
	a.wantRefusedUnread(t, "viewer", http.MethodPut, replace, account.EditMedia, target)
	a.wantRefusedUnread(t, "viewer", http.MethodPut, "/api/media/"+missingMed+"/file?base=1", account.EditMedia, audit.Target{Kind: "media", ID: missingMed})
	if got := len(a.records(t, audit.AccessDenied)); got != 5 {
		t.Errorf("%d records of access.denied, want 5", got)
	}

	// It reads them.
	if res := a.as("viewer").get(replace); res.StatusCode != http.StatusOK {
		t.Errorf("GET %s as a viewer has the status %d, want 200", replace, res.StatusCode)
	}
	if got := len(a.records(t, audit.AccessDenied)); got != 5 {
		t.Errorf("a download made a record of access.denied: %d, want 5", got)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestMediaAuthor shows what an author may do with the files: upload, and replace the file of a medium that it uploaded, and no
// other; the second is refused after the file is received, since who uploaded a medium is read in the Update.
func TestMediaAuthor(t *testing.T) {
	a := newAPIServer(t)
	author, editor := a.people["author"].account.ID, a.people["editor"].account.ID
	as := a.as("author")
	pdf, err := os.ReadFile(filepath.FromSlash(pdfOfSite))
	if err != nil {
		t.Fatal(err)
	}

	mine := result(t, "an upload of the author", as.upload("mine.png", "image/png", testPNG(t, 4, 3)), http.StatusCreated)
	doc := a.stored(t, content.RefMedia, mine.ID)
	if m := a.storedMedium(t, mine.ID); m.UploadedBy != author {
		t.Errorf("the medium is uploaded by %s, want the author %s", m.UploadedBy, author)
	}
	saved := result(t, "a replacement of its own", as.replaceFile(mine.ID, doc.Seq, "image/png", testPNG(t, 5, 5)), http.StatusOK)
	if saved.Version.SavedBy != author || a.storedMedium(t, mine.ID).Width != 5 {
		t.Errorf("the replacement of its own answers %+v", saved)
	}

	// Not those of others: an editor's, and one of the importer, who has no account.
	theirs := a.addFile(t, "editor", "theirs.pdf", "application/pdf", pdf)
	for _, m := range []struct {
		name, id, owner string
		file            []byte
		contentType     string
	}{
		{"a medium of an editor", theirs.ID, editor, pdf, "application/pdf"},
		{"a medium of the importer", sampleLogo, importer, testPNG(t, 6, 6), "image/png"},
	} {
		cur := a.stored(t, content.RefMedia, m.id)
		a.clock.advance(time.Minute)
		n := len(a.records(t, audit.AccessDenied))
		checkProblem(t, "a replacement of "+m.name, as.replaceFile(m.id, cur.Seq, m.contentType, m.file), "forbidden")
		records := a.records(t, audit.AccessDenied)
		if len(records) != n+1 {
			t.Fatalf("the refusal made %d records of access.denied, want 1", len(records)-n)
		}
		if r := records[n]; r.Actor != author || r.Target != (audit.Target{Kind: "media", ID: m.id}) || len(r.Detail) != 1 ||
			r.Detail["permission"] != string(account.EditMedia) {
			t.Errorf("the record of the refusal of %s is %+v, want the permission media.edit and the medium", m.name, r)
		}
		if got := a.stored(t, content.RefMedia, m.id); got.Seq != cur.Seq || got.Hash != cur.Hash {
			t.Errorf("%s was changed by a request that was refused", m.name)
		}
	}
	// The same refusal wherever the base is, since who may is checked before the version.
	checkProblem(t, "a replacement of a medium of an editor with an old base", as.replaceFile(theirs.ID, 1, "application/pdf", pdf), "forbidden")

	// An editor and an administrator replace the files of others.
	cur := a.stored(t, content.RefMedia, mine.ID)
	result(t, "a replacement as an editor", a.as("editor").replaceFile(mine.ID, cur.Seq, "image/png", testPNG(t, 6, 6)), http.StatusOK)
	cur = a.stored(t, content.RefMedia, mine.ID)
	result(t, "a replacement as an admin", a.as("admin").replaceFile(mine.ID, cur.Seq, "image/png", testPNG(t, 7, 7)), http.StatusOK)
	if m := a.storedMedium(t, mine.ID); m.UploadedBy != a.people["admin"].account.ID || m.Width != 7 {
		t.Errorf("the medium is %+v, want the file of the admin", m)
	}
	// What the author uploaded before is no longer its own after the editor and the admin replaced the file.
	cur = a.stored(t, content.RefMedia, mine.ID)
	a.clock.advance(time.Minute)
	checkProblem(t, "a replacement of a file that others replaced", as.replaceFile(mine.ID, cur.Seq, "image/png", testPNG(t, 4, 4)), "forbidden")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestMediaChangesInUpdate shows that the permission is checked again in the Update of the operation, with what the store has
// then: an upload whose account has lost its role while the file was being sent is refused, nothing is saved but the original,
// and a replacement is refused if another account has uploaded the medium since. An account whose credentials have changed is
// answered as one without a session.
func TestMediaChangesInUpdate(t *testing.T) {
	a := newAPIServer(t)
	file := testPNG(t, 40, 40)
	half := file[:len(file)/2]

	t.Run("role of an upload", func(t *testing.T) {
		victim := a.victim(t, "victim0", account.RoleEditor)
		p := a.startPiped(t, "victim0", http.MethodPost, "/api/media?fileName=late.png", "image/png", half)
		waitFor(t, "the upload to be counted", func() bool { return a.inflight.count(victim) == 1 })
		down := account.RoleViewer
		if _, err := a.accounts.Update(t.Context(), account.System, victim, account.Change{Role: &down}); err != nil {
			t.Fatal(err)
		}
		a.clock.advance(time.Minute)
		before, n := a.versions(t), len(a.records(t, audit.AccessDenied))
		p.finish(file[len(half):])
		checkProblem(t, "an upload by an account whose role changes", receive(t, p.result, "the upload"), "forbidden")
		records := a.records(t, audit.AccessDenied)
		if len(records) != n+1 {
			t.Fatalf("the refusal made %d records of access.denied, want 1", len(records)-n)
		}
		if r := records[n]; r.Actor != victim || r.Target != (audit.Target{}) || r.Detail["permission"] != string(account.AddMedia) {
			t.Errorf("the record is %+v, want the actor %s and the permission media.add", r, victim)
		}
		if got := a.versions(t); got != before {
			t.Errorf("the upload has changed the store:\n%s\nwant\n%s", got, before)
		}
	})

	t.Run("role of a replacement", func(t *testing.T) {
		victim := a.victim(t, "victim1", account.RoleEditor)
		created := a.addFile(t, "admin", "r.png", "", testPNG(t, 3, 3))
		doc := a.stored(t, content.RefMedia, created.ID)
		p := a.startPiped(t, "victim1", http.MethodPut, "/api/media/"+created.ID+"/file?base="+strconv.FormatInt(doc.Seq, 10), "image/png", half)
		waitFor(t, "the replacement to be counted", func() bool { return a.inflight.count(victim) == 1 })
		down := account.RoleViewer
		if _, err := a.accounts.Update(t.Context(), account.System, victim, account.Change{Role: &down}); err != nil {
			t.Fatal(err)
		}
		a.clock.advance(time.Minute)
		n := len(a.records(t, audit.AccessDenied))
		p.finish(file[len(half):])
		checkProblem(t, "a replacement by an account whose role changes", receive(t, p.result, "the replacement"), "forbidden")
		records := a.records(t, audit.AccessDenied)
		if len(records) != n+1 || records[n].Actor != victim || records[n].Target != (audit.Target{Kind: "media", ID: created.ID}) ||
			records[n].Detail["permission"] != string(account.EditMedia) {
			t.Errorf("the records are %+v, want one more for %s of the medium", records, account.EditMedia)
		}
		if got := a.stored(t, content.RefMedia, created.ID); got.Seq != doc.Seq {
			t.Errorf("the medium was changed by a replacement that was refused")
		}
	})

	t.Run("credentials", func(t *testing.T) {
		victim := a.victim(t, "victim2", account.RoleEditor)
		p := a.startPiped(t, "victim2", http.MethodPost, "/api/media?fileName=reset.png", "image/png", half)
		waitFor(t, "the upload to be counted", func() bool { return a.inflight.count(victim) == 1 })
		a.clock.advance(time.Minute) // the time of the credentials is that of the clock
		token, err := a.accounts.Reset(t.Context(), account.System, victim)
		if err != nil {
			t.Fatal(err)
		}
		a.seen.token(token)
		before, n := a.versions(t), len(a.records(t, audit.AccessDenied))
		p.finish(file[len(half):])
		res := receive(t, p.result, "the upload")
		checkProblem(t, "an upload by an account whose credentials change", res, "unauthenticated")
		if got := res.Header.Values("Set-Cookie"); !equalValues(got, a.clearLine()) {
			t.Errorf("the answer sets the cookies %q, want the one that clears the session", got)
		}
		if got := a.versions(t); got != before || len(a.records(t, audit.AccessDenied)) != n {
			t.Error("the upload has changed the store, or recorded a refusal: it is not a refusal of a permission")
		}
	})

	t.Run("owner", func(t *testing.T) {
		created := result(t, "an upload", a.as("author").upload("o.png", "image/png", testPNG(t, 3, 3)), http.StatusCreated)
		doc := a.stored(t, content.RefMedia, created.ID)
		author := a.people["author"].account.ID
		p := a.startPiped(t, "author", http.MethodPut, "/api/media/"+created.ID+"/file?base="+strconv.FormatInt(doc.Seq, 10), "image/png", half)
		waitFor(t, "the replacement to be counted", func() bool { return a.inflight.count(author) == 1 })
		// The editor uploads another file for the medium, which makes the editor the account that uploaded it.
		editor := a.people["editor"].account.ID
		other := result(t, "the replacement of the editor", a.as("editor").replaceFile(created.ID, doc.Seq, "image/png", testPNG(t, 8, 8)), http.StatusOK)
		a.clock.advance(time.Minute)
		n := len(a.records(t, audit.AccessDenied))
		p.finish(file[len(half):])
		// The version is old as well, and it is who may that is checked first.
		checkProblem(t, "a replacement of a medium that another account has uploaded since", receive(t, p.result, "the replacement"), "forbidden")
		records := a.records(t, audit.AccessDenied)
		if len(records) != n+1 || records[n].Actor != author || records[n].Target != (audit.Target{Kind: "media", ID: created.ID}) {
			t.Errorf("the records are %+v, want one more for the author", records)
		}
		if got := a.stored(t, content.RefMedia, created.ID); got.Seq != other.Version.Seq || a.storedMedium(t, created.ID).UploadedBy != editor {
			t.Error("the medium is not as the editor left it")
		}
	})
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}
