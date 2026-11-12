package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// checkedCall is an operation of the sample site, with the state that it needs: call does it as op says.
type checkedCall struct {
	call     func(op Op) (Result, error)
	original string // the hash of the file that the operation saves as an original before its Update, or ""
}

// checkedOp is an operation that takes an Op: prepare makes the state that it needs in the store of the sample site, with
// its Editor and no check.
type checkedOp struct {
	name    string
	prepare func(t *testing.T, ed *Editor, s *store.Store) checkedCall
}

// checkedOps lists the operations that take an Op.
func checkedOps() []checkedOp {
	user := Op{By: userID}
	return []checkedOp{
		{"CreateEntry", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			return checkedCall{call: func(op Op) (Result, error) { return ed.CreateEntry(t.Context(), op, content.TypePost, "ja") }}
		}},
		{"SaveRevision", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			entry := newDraft(t, ed, content.TypePage, "ja")
			return checkedCall{call: func(op Op) (Result, error) {
				return ed.SaveRevision(t.Context(), op, entry, "", []byte(`{"schemaVersion":1,"title":"a","slug":"a"}`))
			}}
		}},
		{"CreateTranslation", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			return checkedCall{call: func(op Op) (Result, error) { return ed.CreateTranslation(t.Context(), op, homeJA, "en") }}
		}},
		{"Create", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			data := termJSON("category", "ja", testID(content.RefTranslationGroup, 1), "ニュースレター", "newsletter")
			return checkedCall{call: func(op Op) (Result, error) {
				return ed.Create(t.Context(), op, content.RefTerm, []byte(data))
			}}
		}},
		{"Save", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			cur := read(t, s, content.RefTerm, newsID)
			data := replaceOnce(t, string(cur.Data), `"name": "お知らせ"`, `"name": "ニュース"`)
			return checkedCall{call: func(op Op) (Result, error) {
				return ed.Save(t.Context(), op, content.RefTerm, newsID, cur.Seq, []byte(data))
			}}
		}},
		{"Delete", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			made := must(t)(ed.CreateEntry(t.Context(), user, content.TypePage, "en"))
			return checkedCall{call: func(op Op) (Result, error) {
				return ed.Delete(t.Context(), op, content.RefEntry, made.ID, made.Version.Seq)
			}}
		}},
		{"SaveSettings", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			cur := read(t, s, store.SettingsKind, "")
			data := replaceOnce(t, string(cur.Data), "Webサイトの企画と制作", "Webサイトの企画・制作・運用")
			return checkedCall{call: func(op Op) (Result, error) { return ed.SaveSettings(t.Context(), op, cur.Seq, []byte(data)) }}
		}},
		{"AddMedia", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			file := testPNG(t, 5, 4, 1)
			return checkedCall{
				call: func(op Op) (Result, error) {
					return ed.AddMedia(t.Context(), op, bytes.NewReader(file), "upload.png", "image/png")
				},
				original: fileHash(file),
			}
		}},
		{"ReplaceMedia", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			added := must(t)(ed.AddMedia(t.Context(), user, bytes.NewReader(testPNG(t, 5, 4, 20)), "a.png", ""))
			file := testPNG(t, 6, 5, 21)
			return checkedCall{
				call: func(op Op) (Result, error) {
					return ed.ReplaceMedia(t.Context(), op, added.ID, added.Version.Seq, bytes.NewReader(file), "image/png")
				},
				original: fileHash(file),
			}
		}},
		{"Publish", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
			seq := entrySeq(t, s, draftEN)
			return checkedCall{call: func(op Op) (Result, error) { return ed.Publish(t.Context(), op, draftEN, seq, rev) }}
		}},
		{"Unpublish", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			seq := entrySeq(t, s, postJA)
			return checkedCall{call: func(op Op) (Result, error) { return ed.Unpublish(t.Context(), op, postJA, seq) }}
		}},
		{"Archive", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			seq := entrySeq(t, s, contactJA)
			return checkedCall{call: func(op Op) (Result, error) { return ed.Archive(t.Context(), op, contactJA, seq) }}
		}},
		{"Schedule", func(t *testing.T, ed *Editor, s *store.Store) checkedCall {
			rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
			seq := entrySeq(t, s, draftEN)
			return checkedCall{call: func(op Op) (Result, error) { return ed.Schedule(t.Context(), op, draftEN, seq, rev, careersAt) }}
		}},
	}
}

// operationRecorded reports whether s has a record of the operation ID id.
func operationRecorded(t *testing.T, s *store.Store, id string) bool {
	t.Helper()
	var ok bool
	err := s.View(t.Context(), func(tx *store.Tx) (err error) {
		_, ok, err = tx.Operation(id)
		return err
	})
	if err != nil {
		t.Fatalf("Operation(%q) = %v", id, err)
	}
	return ok
}

// TestOpCheck checks Op.Check of every operation that takes an Op: it is called once, in the Update of the operation before
// it writes anything, and its error, as it is, fails the operation, which leaves nothing but the original of a file that it
// has saved; a retry that returns the recorded result does not call it, nor does another request with the same ID.
func TestOpCheck(t *testing.T) {
	errDenied := errors.New("the caller refuses")
	for _, tt := range checkedOps() {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			c := tt.prepare(t, ed, s)
			var refused, allowed int
			begins := func(tx *store.Tx) {
				t.Helper()
				if tx.Changed() {
					t.Error("the check was called after the operation had written")
				}
			}
			refuse := func(tx *store.Tx) error {
				begins(tx)
				refused++
				return fmt.Errorf("check: %w", errDenied)
			}
			allow := func(tx *store.Tx) error {
				begins(tx)
				allowed++
				_, err := tx.Document(store.SettingsKind, "") // it reads the store
				return err
			}
			counts := func(what string, wantRefused, wantAllowed int) {
				t.Helper()
				if refused != wantRefused || allowed != wantAllowed {
					t.Fatalf("%s: the check that refuses was called %d times and the one that allows %d, want %d and %d",
						what, refused, allowed, wantRefused, wantAllowed)
				}
			}
			id := testOpID(1)
			before, records := snapshot(t, s), len(activity(t, s))
			if c.original != "" && hasOriginal(t, s, c.original) {
				t.Fatalf("the store has the original %s before the operation", c.original)
			}
			unchanged := func(what string) {
				t.Helper()
				if got := snapshot(t, s); got != before {
					t.Errorf("%s has changed the documents or the revisions:\n%s\nwant\n%s", what, got, before)
				}
				if got := activity(t, s); len(got) != records {
					t.Errorf("%s has added the records %+v to the activity log", what, got[records:])
				}
				if operationRecorded(t, s, id) {
					t.Errorf("%s has recorded the operation ID", what)
				}
			}

			// Without an operation ID, and with one, the error of the check is the error of the operation, as it is.
			for i, op := range []Op{{By: userID}, {By: userID, ID: id}} {
				op.Check = refuse
				res, err := c.call(op)
				if !errors.Is(err, errDenied) || err.Error() != "check: the caller refuses" {
					t.Fatalf("the operation with a check that refuses = %+v, %v; want the error of the check, as it is", res, err)
				}
				counts("a check that refuses", i+1, 0)
				unchanged("an operation whose check refuses")
				if c.original != "" && !hasOriginal(t, s, c.original) {
					t.Errorf("the original %s is not in the store: it is saved before the Update, and stays", c.original)
				}
			}

			// A check that allows is called once, and then the operation is recorded with its ID.
			res, err := c.call(Op{By: userID, ID: id, Check: allow})
			if err != nil {
				t.Fatalf("the operation with a check that allows: error = %v", err)
			}
			counts("a check that allows", 2, 1)
			if got := activity(t, s); len(got) != records+1 || !operationRecorded(t, s, id) {
				t.Errorf("the operation added %d records and recorded its ID = %v, want one record, and the ID recorded",
					len(got)-records, operationRecorded(t, s, id))
			}
			after := snapshot(t, s)

			// A retry returns the recorded result without calling the check, also one that would refuse.
			again, err := c.call(Op{By: userID, ID: id, Check: refuse})
			if err != nil || !reflect.DeepEqual(again, res) {
				t.Errorf("a retry of the operation = %+v, %v; want the first result %+v", again, err, res)
			}
			counts("a retry", 2, 1)
			if snapshot(t, s) != after || len(activity(t, s)) != records+1 {
				t.Error("a retry has changed the store")
			}

			// The same ID for another request fails before the check, with an error that wraps ErrOperationReused.
			_, err = c.call(Op{By: otherUser, ID: id, Check: refuse})
			want := fmt.Sprintf("operation ID %q was used for another request", id)
			if !errors.Is(err, ErrOperationReused) || errors.Is(err, errDenied) || err.Error() != want {
				t.Errorf("the same operation ID for another request: error = %v, want %q that wraps ErrOperationReused", err, want)
			}
			counts("another request", 2, 1)
		})
	}
}

// TestOperationReused checks that the error of an operation ID that is used for another request wraps ErrOperationReused
// and nothing else of the errors of the operations, and keeps its text.
func TestOperationReused(t *testing.T) {
	ed, _ := newSample(t)
	op := Op{By: userID, ID: testOpID(9)}
	must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	_, err := ed.CreateEntry(t.Context(), op, content.TypePage, "ja")
	if want := fmt.Sprintf("operation ID %q was used for another request", op.ID); err == nil || err.Error() != want {
		t.Fatalf("CreateEntry() of another type with the same operation ID = %v, want %q", err, want)
	}
	if !errors.Is(err, ErrOperationReused) || errors.Is(err, ErrConflict) || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the error %v wraps ErrOperationReused = %v, ErrConflict = %v and fs.ErrNotExist = %v, want only the first",
			err, errors.Is(err, ErrOperationReused), errors.Is(err, ErrConflict), errors.Is(err, fs.ErrNotExist))
	}
}

// TestEditorDefinitions checks that Schema and ThemeSettings return what New was given.
func TestEditorDefinitions(t *testing.T) {
	schema := content.SiteFile{Name: "site/schema.yaml", Data: sampleFile(t, "site/schema.yaml")}
	th, err := theme.Load(os.DirFS(themeDir))
	if err != nil {
		t.Fatal(err)
	}
	ed := New(nil, schema, th)
	if got := ed.Schema(); !reflect.DeepEqual(got, schema) {
		t.Errorf("Schema() = %+v, want the schema file that New was given, %+v", got, schema)
	}
	if got := ed.ThemeSettings(); got != th.Settings || got == nil || got.DefaultLanguage != "ja" {
		t.Errorf("ThemeSettings() = %+v, want the settings of the theme that New was given, %+v", got, th.Settings)
	}
}
