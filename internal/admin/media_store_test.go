package admin

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// TestDownloadFreesReader shows that a download gives its connection to the daemon back to the client once the original is in
// the spool, while the client has not read any of the file: another read of the store can use it. The file is more than the
// buffers of both ends of a connection hold, so that the answer waits for the client, and the handler has not ended.
func TestDownloadFreesReader(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	url := "http://" + srv.Listener.Addr().String()
	a, _, watch := newWatchedAPIServer(t, url)
	srv.Config.Handler = a.Server
	srv.Start()
	t.Cleanup(srv.Close)
	big := bigPDF(48 << 20)
	added := a.addFile(t, "admin", "big.pdf", "application/pdf", big)
	a.waitBuilds(t)

	host := strings.TrimPrefix(url, "http://")
	request := "GET /api/media/" + added.ID + "/file HTTP/1.1\r\nHost: " + host + "\r\nCookie: " + a.wantCookieName() + "=" +
		a.people["viewer"].token + "\r\nConnection: close\r\n\r\n"
	mark := watch.Mark()
	conn := dialAndSend(t, host, request)
	// The head of the answer comes after the copy into the spool, and after the connection of the store is given back.
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("the response to the download: %v", err)
	}
	// Closing the body would read the rest of the file, and let the handler end.
	defer func() { _ = res.Body.Close() }()
	read, original := watch.ReadSince(mark)
	if read < int64(len(big)) || original == nil {
		t.Fatalf("the reader connections of the store have read %d bytes for a download of %d bytes", read, len(big))
	}

	// A few reads, not one: which of the idle connections a read takes is not what is looked at.
	writes := original.Writes()
	for range 20 {
		// A View takes a connection when it first reads.
		err := a.db.View(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Document(content.RefTerm, newsTerm)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if original.Writes() > writes {
			return
		}
	}
	t.Error("the connection that carried the original has not been used by 20 reads of the store while the client has not read " +
		"the file: the download keeps it")
}

// TestMediaHeadReadsNothing shows that a HEAD of a download opens the original, checks its size, and reads none of it: the
// connections of the store read a small part of the file, and no spool is made.
func TestMediaHeadReadsNothing(t *testing.T) {
	a, _, watch := newWatchedAPIServer(t, httpsOrigin)
	a.waitBuilds(t)
	big := bigPDF(8 << 20)
	added := a.addFile(t, "admin", "big.pdf", "application/pdf", big)
	a.waitBuilds(t)

	mark := watch.Mark()
	res := a.as("viewer").send(http.MethodHead, "/api/media/"+added.ID+"/file", "")
	checkFile(t, "HEAD of a large file", res, true, big, "application/pdf", "attachment; filename=big.pdf")
	if read, _ := watch.ReadSince(mark); read > 1<<20 {
		t.Errorf("a HEAD of a file of %d bytes has made the connections of the store read %d bytes, want a small part of it", len(big), read)
	}
	if files := a.leftovers(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}

	// A GET of it reads it, so that the count tells the two apart.
	mark = watch.Mark()
	checkFile(t, "GET of a large file", a.as("viewer").get("/api/media/"+added.ID+"/file"), false, big, "application/pdf",
		"attachment; filename=big.pdf")
	if read, _ := watch.ReadSince(mark); read < int64(len(big)) {
		t.Errorf("a GET of a file of %d bytes has made the connections of the store read %d bytes, want all of it", len(big), read)
	}
}

// TestDownloadCopyDeadline shows that the copy of an original from the daemon into the spool is bound by the deadline of the
// request: a connection of the store that gives no more bytes in the middle of the file ends the request at its deadline, with
// 503 and with the place of the account given back, and no file is left.
func TestDownloadCopyDeadline(t *testing.T) {
	a, _, watch := newWatchedAPIServer(t, httpsOrigin)
	a.waitBuilds(t)
	added := a.addFile(t, "admin", "big.pdf", "application/pdf", bigPDF(4<<20))
	a.waitBuilds(t)
	viewer := a.people["viewer"].account.ID

	const pattern = "GET /api/media/{id}/file"
	a.deadlinesMu.Lock()
	if _, ok := a.deadlines[pattern]; !ok {
		a.deadlinesMu.Unlock()
		t.Fatalf("no route %q", pattern)
	}
	a.deadlines[pattern] = 2 * time.Second
	a.deadlinesMu.Unlock()

	watch.Arm(1 << 20)
	answer := async(func() *http.Response { return a.as("viewer").get("/api/media/" + added.ID + "/file") })
	res := receiveWithin(t, answer, "the download, which the deadline of its request must end", 20*time.Second)
	checkProblem(t, "a download whose copy stops", res, "busy")
	if watch.Stalls() == 0 {
		t.Error("no read of the store has stopped, so that the copy was not cut by the deadline")
	}
	if n := a.inflight.count(viewer); n != 0 {
		t.Errorf("the account has %d heavy requests after the download is answered, want 0", n)
	}
	if files := a.leftovers(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}
