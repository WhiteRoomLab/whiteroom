package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strconv"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	// sniffBytes is how much of the start of an upload is read to see that its format is a known one, before any of it is saved.
	sniffBytes = 512
	// uploadDeadline is the deadline of an upload: slowTransfer to receive the file, and heavyRun to save it.
	uploadDeadline = slowTransfer + heavyRun
)

// mediaAdd is who may upload a file as a new medium. The other operations on the media are those of mediaKind.
var mediaAdd = docRule{action: account.AddMedia}

// mediaRoutes registers the routes of the files of the media. They count among the heavy requests of the account (countAccount),
// but none of them takes the place of heavy work while the network is waited for: an upload goes to the store as it comes, and
// a download takes the place to read the document alone, and sends the file after it has given the place back.
func (s *Server) mediaRoutes() {
	s.apiWithin("POST /api/media", uploadDeadline, s.uploadMedium)
	s.apiWithin("PUT /api/media/{id}/file", uploadDeadline, s.replaceMediumFile)
	s.apiWithin("GET /api/media/{id}/file", slowTransfer, s.showMediumFile)
}

// uploadMedium is POST /api/media?fileName=: the body is a file, which becomes a new medium, and fileName is the name it was
// uploaded with.
func (s *Server) uploadMedium(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowRule(w, r, p, mediaAdd, "", audit.Target{}) {
		return
	}
	opID, ok := operationID(w, r)
	if !ok {
		return
	}
	q, ok := readQuery(w, r, queryForms{"fileName": func(v string) bool { return v != "" }}, "fileName")
	if !ok {
		return
	}
	s.upload(w, r, p, true, func(ctx context.Context, file io.Reader, declaredType string) (edit.Result, error) {
		op := edit.Op{By: p.Account.ID, ID: opID, Check: mediaAdd.check(p, "", audit.Target{})}
		return s.editor.AddMedia(ctx, op, file, q.Get("fileName"), declaredType)
	})
}

// replaceMediumFile is PUT /api/media/{id}/file?base=: the body is the file that replaces that of the medium, which must be at
// the version of base.
func (s *Server) replaceMediumFile(w http.ResponseWriter, r *http.Request, p account.Principal) {
	c, ok := s.beginChange(w, r, p, &mediaKind, mediaKind.save, queryForms{"base": isBase}, "base")
	if !ok {
		return
	}
	s.upload(w, r, p, false, func(ctx context.Context, file io.Reader, declaredType string) (edit.Result, error) {
		op := edit.Op{By: p.Account.ID, ID: c.opID, Check: mediaKind.save.check(p, c.id, c.named)}
		return s.editor.ReplaceMedia(ctx, op, c.id, baseOf(c.query), file, declaredType)
	})
}

// upload receives the body of r, a file, and gives it to save, which saves it as an original and makes or changes the medium.
// The request counts among the heavy requests of its account, but does not take the place of heavy work: the body is not
// kept in memory, but goes to the store as it comes. Only its first bytes are looked at before that, to refuse a file in no
// known format before anything is written; save finds out what else is wrong with it, and the file is what its contents
// are, whatever the upload says. The answer is 201 with the Location of the new medium if created, and else 200.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, p account.Principal, created bool,
	save func(ctx context.Context, file io.Reader, declaredType string) (edit.Result, error)) {
	release := s.countAccount(w, p)
	if release == nil {
		return
	}
	defer release()
	if r.ContentLength > content.MaxMediaBytes {
		writeProblem(w, http.StatusRequestEntityTooLarge, codeTooLarge, detailTooLarge)
		return
	}
	if err := extendRead(w, slowTransfer); err != nil {
		s.fail(w, r, err)
		return
	}
	body := &readFailure{r: http.MaxBytesReader(unwrap(w), r.Body, content.MaxMediaBytes)}
	head := make([]byte, sniffBytes)
	n, _ := io.ReadFull(body, head)
	head = head[:n]
	if body.err != nil {
		refuseBody(w, body.err)
		return
	}
	// Only a file in no known format is refused here: another error of Probe may only mean that the head is not the whole file.
	if _, err := mediafile.Probe(bytes.NewReader(head)); errors.Is(err, mediafile.ErrUnsupported) {
		_ = extendWrite(w, writeTimeout)
		writeInvalid(w, content.Errors{{Msg: "cannot read the file: " + err.Error()}})
		return
	}
	res, err := save(r.Context(), io.MultiReader(bytes.NewReader(head), body), declaredType(r))
	if err != nil {
		if body.err != nil {
			refuseBody(w, body.err)
		} else {
			s.answerError(w, r, p, err, attempt{})
		}
		return
	}
	out := newSpool(s.store)
	defer func() { _ = out.Close() }()
	if err := writeResult(out, res); err != nil {
		s.fail(w, r, err)
		return
	}
	if created {
		s.sendSpool(w, r, http.StatusCreated, http.Header{"Location": {"/api/media/" + res.ID}}, out)
		return
	}
	s.sendSpool(w, r, http.StatusOK, nil, out)
}

// declaredType returns the media type that the upload says its file has, without parameters, or "" if it says none.
func declaredType(r *http.Request) string {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return mediaType
}

// refuseBody answers the failure err of reading the body of an upload: 413 if it has more than a file may, and else 400.
func refuseBody(w http.ResponseWriter, err error) {
	_ = extendWrite(w, writeTimeout)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeProblem(w, http.StatusRequestEntityTooLarge, codeTooLarge, detailTooLarge)
		return
	}
	writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
}

// showMediumFile is GET /api/media/{id}/file: the original of the medium, whole, as a file to save and not one to show. It
// takes the place of heavy work to read the document, and sends the file after it has given the place back. If the original
// cannot be read once the answer has begun, the connection is aborted: a part of a file must not pass for the whole.
func (s *Server) showMediumFile(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(w, content.RefMedia, id) {
		return
	}
	release := s.countAccount(w, p)
	if release == nil {
		return
	}
	defer release()
	var m struct {
		hash, mimeType, fileName string
		size                     int
	}
	err := s.inPlace(r.Context(), func(ctx context.Context) error {
		return s.store.View(ctx, func(tx *store.Tx) error {
			medium, err := readMedium(tx, id)
			switch {
			case err != nil:
				return err
			case medium == nil:
				return fmt.Errorf("medium %s: this program cannot read its document", id)
			}
			m.hash, m.mimeType, m.fileName, m.size = medium.Hash, medium.MIMEType, medium.FileName, medium.Size
			return nil
		})
	})
	if err != nil {
		s.answerError(w, r, p, err, attempt{})
		return
	}
	f, err := s.openOriginal(m.hash, m.size)
	if err != nil {
		// The medium is there: an original that is missing is a fault of the store, and not a resource that is missing.
		_ = extendWrite(w, writeTimeout)
		s.fail(w, r, fmt.Errorf("the original of %s: %w", id, err))
		return
	}
	defer func() { _ = f.Close() }()
	if err := extendWrite(w, slowTransfer); err != nil {
		s.fail(w, r, err)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": m.fileName})
	if disposition == "" {
		disposition = "attachment"
	}
	h := w.Header()
	h.Set("Content-Type", m.mimeType)
	h.Set("Content-Length", strconv.Itoa(m.size))
	h.Set("Content-Disposition", disposition)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	failure := &readFailure{r: f}
	if _, err := io.Copy(w, failure); err != nil && failure.err != nil {
		s.log.Printf("%s %q: %v", r.Method, r.RequestURI, fmt.Errorf("cannot read the original of %s: %w", id, failure.err))
		panic(http.ErrAbortHandler)
	}
}

// openOriginal opens the original with the hash, and checks that it has the size that the document of the medium says.
func (s *Server) openOriginal(hash string, size int) (fs.File, error) {
	f, err := s.store.OpenOriginal(hash)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	switch {
	case err != nil:
	case info.Size() != int64(size):
		err = fmt.Errorf("it has %d bytes and the document says %d", info.Size(), size)
	default:
		return f, nil
	}
	_ = f.Close()
	return nil, err
}
