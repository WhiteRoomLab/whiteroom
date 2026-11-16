package store

import "github.com/WhiteRoomLab/whiteroom/internal/content"

// DocumentID names a document by its kind and its ID, which is "" for the settings document.
type DocumentID struct {
	Kind content.RefKind
	ID   string
}

// Ahead names what the calls that follow a ReadAhead read: Summary of each of Summaries, Document of each of Documents,
// and Revision of each of Revisions.
type Ahead struct {
	Summaries, Documents []DocumentID
	Revisions            []string
}

// ReadAhead reads from the daemon, in windows of requests that it sends before it reads their replies, what Summary of the
// documents of a.Summaries, Document of those of a.Documents and Revision of those of a.Revisions will read: the rows,
// and the data of the versions and the revisions that are there, so that those calls then ask the daemon nothing. It
// changes no result and no error of any call: what it could not read, the call reads again and reports as it would
// without it, and a name that nothing has is read as missing. Its own error is that of a connection to the daemon that
// fails, of a context that has ended or of a transaction that has ended. It works in a View and in an Update, where the
// writes of the Update still come first. It drops the data that an earlier ReadAhead has read and that no call has
// taken: call it for a few hundred documents at a time, and read them before the next call.
func (t *Tx) ReadAhead(a Ahead) error {
	if err := t.check(false); err != nil {
		return err
	}
	return t.b.readAhead(a.Summaries, a.Documents, a.Revisions)
}
