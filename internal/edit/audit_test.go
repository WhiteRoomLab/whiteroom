package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// activity returns the records of the activity log of s, from the oldest.
func activity(t *testing.T, s *store.Store) []audit.Record {
	t.Helper()
	var out []audit.Record
	err := s.ReadAudit(t.Context(), audit.Filter{Log: audit.ActivityLog}, func(r audit.Record) bool {
		out = append(out, r)
		return true
	})
	if err != nil {
		t.Fatalf("ReadAudit() = %v", err)
	}
	return out
}

// verifyChains fails the test unless the chains of the logs of s are as the store wrote them.
func verifyChains(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.VerifyAudit(t.Context(), nil); err != nil {
		t.Errorf("VerifyAudit() = %v", err)
	}
}

// wantRecord is the record that an operation is to add to the activity log, but for its number, the hash of the record
// before it and its time, which the store sets.
type wantRecord struct {
	action    audit.Action
	actor     string
	target    audit.Target
	operation string
	changed   bool
	before    audit.Version
	after     audit.Version
	revision  string
	status    string
	detail    map[string]string
}

// newWant returns the record of an operation by userID that has saved something, without an operation ID.
func newWant(action audit.Action, target audit.Target) wantRecord {
	return wantRecord{action: action, actor: userID, target: target, changed: true}
}

func targetOf(kind content.RefKind, id string) audit.Target {
	return audit.Target{Kind: string(kind), ID: id}
}

func versionOf(v store.Version) audit.Version { return audit.Version{Seq: v.Seq, Hash: v.Hash} }

func seqOf(seq int64) audit.Version { return audit.Version{Seq: seq} }

// checkRecord checks that got is the record want, that the client and the other items of the login history are none, that
// the store has encoded it, and that it is small: it points at the documents and does not copy them.
func checkRecord(t *testing.T, got audit.Record, want wantRecord) {
	t.Helper()
	data, err := audit.Encode(got)
	if err != nil {
		t.Fatalf("Encode(%+v) = %v", got, err)
	}
	if len(data) > 2048 {
		t.Errorf("the record has %d bytes, want it to point at the documents and not to copy them: %s", len(data), data)
	}
	if got.N == 0 || got.At.IsZero() {
		t.Errorf("the record %+v has no number or no time", got)
	}
	got.N, got.Prev, got.At = 0, "", time.Time{}
	wantRec := audit.Record{
		Log: audit.ActivityLog, Actor: want.actor, Action: want.action, Target: want.target, Operation: want.operation,
		Changed: want.changed, Before: want.before, After: want.after, Revision: want.revision, Status: want.status,
		Detail: want.detail,
	}
	if !reflect.DeepEqual(got, wantRec) {
		t.Errorf("the record is\n%+v\nwant\n%+v", got, wantRec)
	}
}

// step is an operation that a test runs, and the record that it is to add to the activity log.
type step struct {
	call    func() (Result, error)
	want    func(res Result) wantRecord
	savedAt func(res Result) time.Time // the time of the Update, if the record is not that of a version that the result has
}

// runStep runs st in s, which must add exactly one record to the activity log, in the Update that saves, and checks it.
func runStep(t *testing.T, s *store.Store, st step) wantRecord {
	t.Helper()
	before := activity(t, s)
	res, err := st.call()
	if err != nil {
		t.Fatalf("the operation failed: %v", err)
	}
	after := activity(t, s)
	if len(after) != len(before)+1 {
		t.Fatalf("the operation added %d records to the activity log, want 1: %+v", len(after)-len(before), after[len(before):])
	}
	got, want := after[len(after)-1], st.want(res)
	checkRecord(t, got, want)

	// The record is made at the time of the Update that saves, as the versions that it saves are.
	var at time.Time
	switch {
	case st.savedAt != nil:
		at = st.savedAt(res)
	case want.after.Seq != 0:
		at = res.Version.SavedAt
	case want.changed && want.revision != "":
		err := s.View(t.Context(), func(tx *store.Tx) error {
			r, err := tx.Revision(want.revision)
			at = r.SavedAt
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !at.IsZero() && !got.At.Equal(at) {
		t.Errorf("the record is at %s, want %s, the time of the version that the Update saved", got.At, at)
	}
	if n := len(before); n > 0 && got.N != before[n-1].N+1 {
		t.Errorf("the record is number %d, want the one after %d", got.N, before[n-1].N)
	}
	verifyChains(t, s)
	return want
}

// auditCase is an operation that adds one record to the activity log. prepare makes the store and what it needs, which
// may add records of its own, and returns the operation.
type auditCase struct {
	name    string
	prepare func(t *testing.T) (*store.Store, step)
}

// sample returns a prepare for the sample site and an Editor for it.
func sample(f func(t *testing.T, ed *Editor, s *store.Store) step) func(t *testing.T) (*store.Store, step) {
	return func(t *testing.T) (*store.Store, step) {
		ed, s := newSample(t)
		return s, f(t, ed, s)
	}
}

// noWarnings fails the test if res has warnings.
func noWarnings(t *testing.T, res Result) {
	t.Helper()
	if len(res.Warnings) != 0 {
		t.Fatalf("the result has the warnings %q, want none", lines(res.Warnings))
	}
}

// warnings fails the test unless res has n warnings, and returns n for the detail of a record.
func warnings(t *testing.T, res Result, n int) string {
	t.Helper()
	if len(res.Warnings) != n {
		t.Fatalf("the result has the warnings %q, want %d", lines(res.Warnings), n)
	}
	return strconv.Itoa(n)
}

// The documents of each kind that Create makes, with the actions that record it.
var auditCreates = []struct {
	kind   content.RefKind
	action audit.Action
	data   string
}{
	{content.RefTerm, audit.TermCreate, termJSON("category", "ja", testID(content.RefTranslationGroup, 1), "ニュースレター", "newsletter")},
	{content.RefAuthor, audit.AuthorCreate, authorJSON("佐藤 花子", "sato")},
	{content.RefMenu, audit.MenuCreate, menuJSON("sidebar", "ja", `[{ "label": "ホーム", "href": "/" }]`)},
	{content.RefForm, audit.FormCreate, formJSON},
	{content.RefRedirect, audit.RedirectCreate, redirectJSON("/old-contact/", "/contact/")},
}

// The documents of the sample site that Save changes, with the actions that record it.
var auditSaves = []struct {
	kind     content.RefKind
	id       string
	action   audit.Action
	old, new string
}{
	{content.RefTerm, newsID, audit.TermSave, `"name": "お知らせ"`, `"name": "ニュース"`},
	{content.RefAuthor, "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5", audit.AuthorSave, `"name": "山田 太郎"`, `"name": "山田 次郎"`},
	{content.RefMenu, "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4", audit.MenuSave, `"title": "フッター"`, `"title": "フッターのメニュー"`},
	{content.RefForm, "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6", audit.FormSave, `"title": "お問い合わせ"`, `"title": "お問い合わせフォーム"`},
	{content.RefRedirect, "rdr_01JC0000000000000000000001", audit.RedirectSave, `"status": 301`, `"status": 302`},
	{content.RefMedia, jpegMedium, audit.MediaSave, `"ja": "会社外観"`, `"ja": "本社の外観"`},
}

// datelessPost is a revision of a post that has no date, which a save warns of and a publish gives one.
const datelessPost = `{"schemaVersion":1,"title":"今日の投稿","slug":"today","terms":{"tag":["` + tagJA + `"]},` +
	`"body":[{"type":"paragraph","id":"b_00000001","content":[{"type":"text","text":"本文"}]}]}`

// The actions of the activity log that the operations of this package record.
var editActions = []audit.Action{
	audit.EntryCreate, audit.EntrySaveRevision, audit.EntryCreateTranslation, audit.EntryDelete, audit.EntryPublish,
	audit.EntryUnpublish, audit.EntryArchive, audit.EntrySchedule, audit.EntryPublishScheduled,
	audit.TermCreate, audit.TermSave, audit.TermDelete,
	audit.AuthorCreate, audit.AuthorSave, audit.AuthorDelete,
	audit.MenuCreate, audit.MenuSave, audit.MenuDelete,
	audit.FormCreate, audit.FormSave, audit.FormDelete,
	audit.RedirectCreate, audit.RedirectSave, audit.RedirectDelete,
	audit.MediaAdd, audit.MediaReplace, audit.MediaSave, audit.MediaDelete,
	audit.SettingsSave, audit.SiteImport,
}

// auditSaved returns the cases of the operations that save something.
func auditSaved() []auditCase {
	op := Op{By: userID}
	var cases []auditCase
	add := func(name string, f func(t *testing.T, ed *Editor, s *store.Store) step) {
		cases = append(cases, auditCase{name, sample(f)})
	}

	add("CreateEntry", func(t *testing.T, ed *Editor, s *store.Store) step {
		withID := Op{By: userID, ID: testOpID(1)}
		number := maxNumber(t, s) + 1
		return step{
			call: func() (Result, error) { return ed.CreateEntry(t.Context(), withID, content.TypePost, "ja") },
			want: func(res Result) wantRecord {
				w := newWant(audit.EntryCreate, targetOf(content.RefEntry, res.ID))
				w.operation, w.after = withID.ID, versionOf(res.Version)
				w.detail = map[string]string{"number": strconv.Itoa(number)}
				return w
			},
		}
	})
	add("SaveRevision", func(t *testing.T, ed *Editor, s *store.Store) step {
		return step{
			call: func() (Result, error) { return ed.SaveRevision(t.Context(), op, draftEN, "", []byte(careersRevision)) },
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.EntrySaveRevision, targetOf(content.RefEntry, draftEN))
				w.revision = res.Revision
				return w
			},
		}
	})
	add("SaveRevision on another revision", func(t *testing.T, ed *Editor, s *store.Store) step {
		first := saveRevision(t, ed, s, draftEN, careersRevision).Revision
		return step{
			call: func() (Result, error) {
				return ed.SaveRevision(t.Context(), op, draftEN, first, []byte(`{"schemaVersion":1,"title":"Careers 2","slug":"careers"}`))
			},
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.EntrySaveRevision, targetOf(content.RefEntry, draftEN))
				w.revision, w.detail = res.Revision, map[string]string{"base": first}
				return w
			},
		}
	})
	add("SaveRevision with a warning", func(t *testing.T, ed *Editor, s *store.Store) step {
		entry := newDraft(t, ed, content.TypePost, "ja")
		return step{
			call: func() (Result, error) { return ed.SaveRevision(t.Context(), op, entry, "", []byte(datelessPost)) },
			want: func(res Result) wantRecord {
				w := newWant(audit.EntrySaveRevision, targetOf(content.RefEntry, entry))
				w.revision, w.detail = res.Revision, map[string]string{"warnings": warnings(t, res, 1)}
				return w
			},
		}
	})
	add("CreateTranslation", func(t *testing.T, ed *Editor, s *store.Store) step {
		number := maxNumber(t, s) + 1
		return step{
			call: func() (Result, error) { return ed.CreateTranslation(t.Context(), op, homeJA, "en") },
			want: func(res Result) wantRecord {
				w := newWant(audit.EntryCreateTranslation, targetOf(content.RefEntry, res.ID))
				w.after, w.revision = versionOf(res.Version), res.Revision
				w.detail = map[string]string{"number": strconv.Itoa(number), "warnings": warnings(t, res, 1)}
				return w
			},
		}
	})
	add("Delete of an entry", func(t *testing.T, ed *Editor, s *store.Store) step {
		made := must(t)(ed.CreateEntry(t.Context(), op, content.TypePage, "en"))
		return step{
			call: func() (Result, error) { return ed.Delete(t.Context(), op, content.RefEntry, made.ID, made.Version.Seq) },
			want: func(res Result) wantRecord {
				if res.Version.Hash != "" {
					t.Fatalf("Delete() = %+v, want the version of a deletion, which has no hash", res)
				}
				w := newWant(audit.EntryDelete, targetOf(content.RefEntry, made.ID))
				w.before, w.after = seqOf(made.Version.Seq), seqOf(res.Version.Seq)
				return w
			},
		}
	})

	for _, tt := range auditCreates {
		add("Create of a "+string(tt.kind), func(t *testing.T, ed *Editor, s *store.Store) step {
			return step{
				call: func() (Result, error) { return ed.Create(t.Context(), op, tt.kind, []byte(tt.data)) },
				want: func(res Result) wantRecord {
					noWarnings(t, res)
					w := newWant(tt.action, targetOf(tt.kind, res.ID))
					w.after, w.detail = versionOf(res.Version), map[string]string{"publishes": "true"}
					return w
				},
			}
		})
	}
	for _, tt := range auditSaves {
		add("Save of a "+string(tt.kind), func(t *testing.T, ed *Editor, s *store.Store) step {
			cur := read(t, s, tt.kind, tt.id)
			data := replaceOnce(t, string(cur.Data), tt.old, tt.new)
			return step{
				call: func() (Result, error) { return ed.Save(t.Context(), op, tt.kind, tt.id, cur.Seq, []byte(data)) },
				want: func(res Result) wantRecord {
					noWarnings(t, res)
					w := newWant(tt.action, targetOf(tt.kind, tt.id))
					w.before, w.after, w.detail = seqOf(cur.Seq), versionOf(res.Version), map[string]string{"publishes": "true"}
					return w
				},
			}
		})
	}
	for _, tt := range auditCreates {
		del := map[content.RefKind]audit.Action{
			content.RefTerm: audit.TermDelete, content.RefAuthor: audit.AuthorDelete, content.RefMenu: audit.MenuDelete,
			content.RefForm: audit.FormDelete, content.RefRedirect: audit.RedirectDelete,
		}[tt.kind]
		add("Delete of a "+string(tt.kind), func(t *testing.T, ed *Editor, s *store.Store) step {
			made := must(t)(ed.Create(t.Context(), op, tt.kind, []byte(tt.data)))
			return step{
				call: func() (Result, error) { return ed.Delete(t.Context(), op, tt.kind, made.ID, made.Version.Seq) },
				want: func(res Result) wantRecord {
					noWarnings(t, res)
					w := newWant(del, targetOf(tt.kind, made.ID))
					w.before, w.after, w.detail = seqOf(made.Version.Seq), seqOf(res.Version.Seq), map[string]string{"publishes": "true"}
					return w
				},
			}
		})
	}
	add("Delete of a medium that a draft uses", func(t *testing.T, ed *Editor, s *store.Store) step {
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 6, 5, 15)), "draft.png", ""))
		entry := newDraft(t, ed, content.TypePost, "ja")
		must(t)(ed.SaveRevision(t.Context(), op, entry, "",
			[]byte(`{"schemaVersion":1,"title":"Draft","slug":"draft","featuredImage":"`+added.ID+`"}`)))
		return step{
			call: func() (Result, error) {
				return ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
			},
			want: func(res Result) wantRecord {
				w := newWant(audit.MediaDelete, targetOf(content.RefMedia, added.ID))
				w.before, w.after = seqOf(added.Version.Seq), seqOf(res.Version.Seq)
				w.detail = map[string]string{"publishes": "true", "warnings": warnings(t, res, 1)}
				return w
			},
		}
	})
	add("SaveSettings", func(t *testing.T, ed *Editor, s *store.Store) step {
		cur := read(t, s, store.SettingsKind, "")
		data := replaceOnce(t, string(cur.Data), "Webサイトの企画と制作", "Webサイトの企画・制作・運用")
		return step{
			call: func() (Result, error) { return ed.SaveSettings(t.Context(), op, cur.Seq, []byte(data)) },
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.SettingsSave, audit.Target{Kind: "settings"})
				w.before, w.after, w.detail = seqOf(cur.Seq), versionOf(res.Version), map[string]string{"publishes": "true"}
				return w
			},
		}
	})

	add("AddMedia", func(t *testing.T, ed *Editor, s *store.Store) step {
		file := testPNG(t, 5, 4, 1)
		return step{
			call: func() (Result, error) {
				return ed.AddMedia(t.Context(), op, bytes.NewReader(file), "upload.png", "image/png")
			},
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.MediaAdd, targetOf(content.RefMedia, res.ID))
				w.after = versionOf(res.Version)
				w.detail = map[string]string{
					"hash": fileHash(file), "size": strconv.Itoa(len(file)), "mimeType": "image/png", "declaredType": "image/png",
					"fileName": "upload.png",
				}
				return w
			},
		}
	})
	add("AddMedia of a file that is not the type it was uploaded as, with a name that is normalized", func(t *testing.T, ed *Editor, s *store.Store) step {
		file := testPDF(3)
		return step{
			call: func() (Result, error) {
				return ed.AddMedia(t.Context(), op, bytes.NewReader(file), "  a\tb.pdf ", "image/jpeg")
			},
			want: func(res Result) wantRecord {
				w := newWant(audit.MediaAdd, targetOf(content.RefMedia, res.ID))
				w.after = versionOf(res.Version)
				w.detail = map[string]string{
					"hash": fileHash(file), "size": strconv.Itoa(len(file)), "mimeType": "application/pdf", "declaredType": "image/jpeg",
					"fileName": "ab.pdf", "warnings": warnings(t, res, 1),
				}
				return w
			},
		}
	})
	add("AddMedia with the longest file name", func(t *testing.T, ed *Editor, s *store.Store) step {
		file, name := testPNG(t, 5, 4, 2), strings.Repeat("a", content.MaxFileNameBytes-len(".png"))+".png"
		return step{
			call: func() (Result, error) { return ed.AddMedia(t.Context(), op, bytes.NewReader(file), name, "") },
			want: func(res Result) wantRecord {
				w := newWant(audit.MediaAdd, targetOf(content.RefMedia, res.ID))
				w.after = versionOf(res.Version)
				w.detail = map[string]string{
					"hash": fileHash(file), "size": strconv.Itoa(len(file)), "mimeType": "image/png", "fileName": name,
				}
				return w
			},
		}
	})
	// The type that an upload says is the caller's own: a record cuts it, and replaces what is not text.
	for _, tt := range []struct{ name, declared, want string }{
		{"a long type", "image/" + strings.Repeat("x", 400), "image/" + strings.Repeat("x", 249)},
		{"a type with control characters", "image/\x00png\x7f\n", "image/" + string(rune(0xFFFD)) + "png" + string(rune(0xFFFD)) + "\n"},
		{"a type that is not UTF-8", "image/\xff", "image/" + string(rune(0xFFFD))},
	} {
		add("AddMedia with "+tt.name, func(t *testing.T, ed *Editor, s *store.Store) step {
			file := testPNG(t, 5, 4, 3)
			return step{
				call: func() (Result, error) {
					return ed.AddMedia(t.Context(), op, bytes.NewReader(file), "a.png", tt.declared)
				},
				want: func(res Result) wantRecord {
					w := newWant(audit.MediaAdd, targetOf(content.RefMedia, res.ID))
					w.after = versionOf(res.Version)
					w.detail = map[string]string{
						"hash": fileHash(file), "size": strconv.Itoa(len(file)), "mimeType": "image/png", "declaredType": tt.want,
						"fileName": "a.png", "warnings": warnings(t, res, 1),
					}
					return w
				},
			}
		})
	}
	add("ReplaceMedia", func(t *testing.T, ed *Editor, s *store.Store) step {
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 5, 4, 20)), "a.png", ""))
		file := testPNG(t, 6, 5, 21)
		return step{
			call: func() (Result, error) {
				return ed.ReplaceMedia(t.Context(), op, added.ID, added.Version.Seq, bytes.NewReader(file), "image/png")
			},
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.MediaReplace, targetOf(content.RefMedia, added.ID))
				w.before, w.after = seqOf(added.Version.Seq), versionOf(res.Version)
				w.detail = map[string]string{
					"hash": fileHash(file), "size": strconv.Itoa(len(file)), "mimeType": "image/png", "declaredType": "image/png",
					"publishes": "true",
				}
				return w
			},
		}
	})

	add("Publish", func(t *testing.T, ed *Editor, s *store.Store) step {
		rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
		seq := entrySeq(t, s, draftEN)
		return step{
			call: func() (Result, error) { return ed.Publish(t.Context(), op, draftEN, seq, rev) },
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.EntryPublish, targetOf(content.RefEntry, draftEN))
				w.before, w.after, w.revision, w.status = seqOf(seq), versionOf(res.Version), rev, "published"
				w.detail = map[string]string{"publishes": "true"}
				return w
			},
		}
	})
	add("Publish with a redirect from the old path", func(t *testing.T, ed *Editor, s *store.Store) step {
		saved := saveRevision(t, ed, s, postJA, postRevision(t, postSlug, `"slug": "launch"`))
		seq := entrySeq(t, s, postJA)
		return step{
			call: func() (Result, error) {
				return ed.Publish(t.Context(), Op{By: userID, ID: testOpID(2)}, postJA, seq, saved.Revision)
			},
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				if res.Redirect == "" {
					t.Fatalf("Publish() = %+v, want the redirect from the old path", res)
				}
				w := newWant(audit.EntryPublish, targetOf(content.RefEntry, postJA))
				w.operation, w.before, w.after = testOpID(2), seqOf(seq), versionOf(res.Version)
				w.revision, w.status = saved.Revision, "published"
				w.detail = map[string]string{"publishes": "true", "redirect": res.Redirect}
				return w
			},
		}
	})
	add("Publish of a post without a date", func(t *testing.T, ed *Editor, s *store.Store) step {
		entry := newDraft(t, ed, content.TypePost, "ja")
		saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(datelessPost)))
		seq := entrySeq(t, s, entry)
		return step{
			call: func() (Result, error) { return ed.Publish(t.Context(), op, entry, seq, saved.Revision) },
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				if res.Revision == saved.Revision {
					t.Fatalf("Publish() = %+v, want a new revision that has the date", res)
				}
				// The revision that is published is the copy that has the date, not the one that was given.
				w := newWant(audit.EntryPublish, targetOf(content.RefEntry, entry))
				w.before, w.after = seqOf(seq), versionOf(res.Version)
				w.revision, w.status = res.Revision, "published"
				w.detail = map[string]string{"publishes": "true"}
				return w
			},
		}
	})
	add("Unpublish", func(t *testing.T, ed *Editor, s *store.Store) step {
		seq := entrySeq(t, s, postJA)
		return step{
			call: func() (Result, error) { return ed.Unpublish(t.Context(), op, postJA, seq) },
			want: func(res Result) wantRecord {
				w := newWant(audit.EntryUnpublish, targetOf(content.RefEntry, postJA))
				w.before, w.after, w.status = seqOf(seq), versionOf(res.Version), "draft"
				w.detail = map[string]string{"publishes": "true", "warnings": warnings(t, res, 1)}
				return w
			},
		}
	})
	add("Archive", func(t *testing.T, ed *Editor, s *store.Store) step {
		seq := entrySeq(t, s, contactJA)
		return step{
			call: func() (Result, error) { return ed.Archive(t.Context(), op, contactJA, seq) },
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.EntryArchive, targetOf(content.RefEntry, contactJA))
				w.before, w.after, w.status = seqOf(seq), versionOf(res.Version), "archived"
				w.detail = map[string]string{"publishes": "true"}
				return w
			},
		}
	})
	add("Archive of a draft", func(t *testing.T, ed *Editor, s *store.Store) step {
		seq := entrySeq(t, s, draftEN)
		return step{
			call: func() (Result, error) { return ed.Archive(t.Context(), op, draftEN, seq) },
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.EntryArchive, targetOf(content.RefEntry, draftEN))
				w.before, w.after, w.status = seqOf(seq), versionOf(res.Version), "archived"
				return w
			},
		}
	})
	add("Schedule", func(t *testing.T, ed *Editor, s *store.Store) step {
		rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
		seq := entrySeq(t, s, draftEN)
		return step{
			// A time in another time zone, with a fraction of a second, which the record has in UTC to the second.
			call: func() (Result, error) {
				return ed.Schedule(t.Context(), op, draftEN, seq, rev, careersAt.In(jst).Add(999*time.Millisecond))
			},
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				w := newWant(audit.EntrySchedule, targetOf(content.RefEntry, draftEN))
				w.before, w.after, w.revision, w.status = seqOf(seq), versionOf(res.Version), rev, "scheduled"
				w.detail = map[string]string{"at": "2026-11-07T11:00:00Z"}
				return w
			},
		}
	})
	add("PublishScheduled", func(t *testing.T, ed *Editor, s *store.Store) step {
		// Scheduled by another user than the one who saved the revision: the entry is saved by the one who scheduled it.
		rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
		must(t)(ed.Schedule(t.Context(), Op{By: otherUser}, draftEN, entrySeq(t, s, draftEN), rev, careersAt))
		return step{
			call: func() (Result, error) { return ed.PublishScheduled(t.Context(), draftEN, careersAt) },
			want: func(res Result) wantRecord {
				noWarnings(t, res)
				if res.Version.SavedBy != otherUser {
					t.Fatalf("PublishScheduled() = %+v, want it saved by %s", res, otherUser)
				}
				w := newWant(audit.EntryPublishScheduled, targetOf(content.RefEntry, draftEN))
				w.actor, w.after, w.revision, w.status = audit.System, versionOf(res.Version), rev, "published"
				w.detail = map[string]string{"publishes": "true", "scheduledBy": otherUser}
				return w
			},
		}
	})

	cases = append(cases, auditCase{"Publish that deletes the redirects to the page from its new path", func(t *testing.T) (*store.Store, step) {
		ed, s, removed := jobsSite(t)
		return s, publishJobs(t, ed, s, removed)
	}}, auditCase{"Publish that deletes more redirects than a record names", func(t *testing.T) (*store.Store, step) {
		ed, s, removed := jobsSite(t)
		for n := 7; n <= 13; n++ {
			id := testID(content.RefRedirect, n)
			put(t, s, content.RefRedirect, id, redirectDoc(t, content.Redirect{ID: id, From: "/en/jobs/", To: draftEN, Status: 302}))
			removed = append(removed, id)
		}
		return s, publishJobs(t, ed, s, removed)
	}}, auditCase{"Import", func(t *testing.T) (*store.Store, step) {
		s := newStore(t)
		return s, step{
			call: func() (Result, error) {
				_, err := Import(t.Context(), s, openSample(t), userID)
				return Result{}, err
			},
			want: func(Result) wantRecord {
				w := newWant(audit.SiteImport, audit.Target{})
				w.detail = importedCounts(t, s)
				return w
			},
			// The settings are saved in the Update that imports, and are the only document that is not a count.
			savedAt: func(Result) time.Time { return read(t, s, store.SettingsKind, "").SavedAt },
		}
	}})
	return cases
}

// publishJobs publishes the page draftEN of jobsSite in the path /en/jobs/, which the redirects removed are from.
func publishJobs(t *testing.T, ed *Editor, s *store.Store, removed []string) step {
	saved := saveRevision(t, ed, s, draftEN, jobsRevision)
	seq := entrySeq(t, s, draftEN)
	return step{
		call: func() (Result, error) { return ed.Publish(t.Context(), Op{By: userID}, draftEN, seq, saved.Revision) },
		want: func(res Result) wantRecord {
			w := newWant(audit.EntryPublish, targetOf(content.RefEntry, draftEN))
			w.before, w.after, w.revision, w.status = seqOf(seq), versionOf(res.Version), saved.Revision, "published"
			list := strings.Join(removed, ", ")
			if len(removed) > maxListed {
				list = strings.Join(removed[:maxListed], ", ") + fmt.Sprintf(" and %d more", len(removed)-maxListed)
			}
			w.detail = map[string]string{
				"publishes": "true", "warnings": warnings(t, res, len(removed)), "redirectsDeleted": list,
			}
			return w
		},
	}
}

// importedCounts returns the detail of the record of the import of the sample site, from the files of the sample and the
// revisions that the store has.
func importedCounts(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	count := func(dir string) string {
		files, err := os.ReadDir(filepath.Join(sampleDir, dir))
		if err != nil {
			t.Fatal(err)
		}
		return strconv.Itoa(len(files))
	}
	revisions := 0
	err := s.View(t.Context(), func(tx *store.Tx) error {
		entries, err := tx.Documents(content.RefEntry)
		if err != nil {
			return err
		}
		for _, e := range entries {
			revs, err := tx.Revisions(e.ID)
			if err != nil {
				return err
			}
			revisions += len(revs)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if revisions == 0 {
		t.Fatal("the imported site has no revisions")
	}
	return map[string]string{
		"entry": count("entries"), "term": count("terms"), "media": count("media"), "author": count("authors"),
		"menu": count("menus"), "form": count("forms"), "redirect": count("redirects"),
		"revisions": strconv.Itoa(revisions), "originals": count("files"),
	}
}

// auditNoOps returns the cases of the operations that succeed and save nothing, which record that they did not.
func auditNoOps() []auditCase {
	op := Op{By: userID}
	var cases []auditCase
	add := func(name string, f func(t *testing.T, ed *Editor, s *store.Store) step) {
		cases = append(cases, auditCase{name, sample(f)})
	}

	add("Save of the same bytes", func(t *testing.T, ed *Editor, s *store.Store) step {
		cur := read(t, s, content.RefTerm, newsID)
		return step{
			call: func() (Result, error) { return ed.Save(t.Context(), op, content.RefTerm, newsID, cur.Seq, cur.Data) },
			want: func(res Result) wantRecord {
				w := newWant(audit.TermSave, targetOf(content.RefTerm, newsID))
				w.changed, w.before = false, seqOf(cur.Seq)
				return w
			},
		}
	})
	add("SaveSettings of the same bytes", func(t *testing.T, ed *Editor, s *store.Store) step {
		cur := read(t, s, store.SettingsKind, "")
		return step{
			call: func() (Result, error) { return ed.SaveSettings(t.Context(), op, cur.Seq, cur.Data) },
			want: func(res Result) wantRecord {
				w := newWant(audit.SettingsSave, audit.Target{Kind: "settings"})
				w.changed, w.before = false, seqOf(cur.Seq)
				return w
			},
		}
	})
	add("ReplaceMedia with the same file", func(t *testing.T, ed *Editor, s *store.Store) step {
		file := testPNG(t, 5, 4, 22)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(file), "a.png", ""))
		return step{
			call: func() (Result, error) {
				return ed.ReplaceMedia(t.Context(), op, added.ID, added.Version.Seq, bytes.NewReader(file), "")
			},
			want: func(res Result) wantRecord {
				w := newWant(audit.MediaReplace, targetOf(content.RefMedia, added.ID))
				w.changed, w.before = false, seqOf(added.Version.Seq)
				w.detail = map[string]string{"hash": fileHash(file), "size": strconv.Itoa(len(file)), "mimeType": "image/png"}
				return w
			},
		}
	})
	add("Publish of what is published", func(t *testing.T, ed *Editor, s *store.Store) step {
		rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
		first := must(t)(ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev))
		return step{
			call: func() (Result, error) { return ed.Publish(t.Context(), op, draftEN, first.Version.Seq, rev) },
			want: func(res Result) wantRecord {
				w := newWant(audit.EntryPublish, targetOf(content.RefEntry, draftEN))
				w.changed, w.before, w.revision, w.status = false, seqOf(first.Version.Seq), rev, "published"
				return w
			},
		}
	})
	add("Unpublish of a draft", func(t *testing.T, ed *Editor, s *store.Store) step {
		seq := entrySeq(t, s, draftEN)
		return step{
			call: func() (Result, error) { return ed.Unpublish(t.Context(), op, draftEN, seq) },
			want: func(res Result) wantRecord {
				w := newWant(audit.EntryUnpublish, targetOf(content.RefEntry, draftEN))
				w.changed, w.before, w.status = false, seqOf(seq), "draft"
				return w
			},
		}
	})
	add("Archive of what is archived", func(t *testing.T, ed *Editor, s *store.Store) step {
		first := must(t)(ed.Archive(t.Context(), op, draftEN, entrySeq(t, s, draftEN)))
		return step{
			call: func() (Result, error) { return ed.Archive(t.Context(), op, draftEN, first.Version.Seq) },
			want: func(res Result) wantRecord {
				w := newWant(audit.EntryArchive, targetOf(content.RefEntry, draftEN))
				w.changed, w.before, w.status = false, seqOf(first.Version.Seq), "archived"
				return w
			},
		}
	})
	add("Schedule of what is scheduled", func(t *testing.T, ed *Editor, s *store.Store) step {
		rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
		first := must(t)(ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, careersAt))
		return step{
			call: func() (Result, error) {
				return ed.Schedule(t.Context(), op, draftEN, first.Version.Seq, rev, careersAt)
			},
			want: func(res Result) wantRecord {
				w := newWant(audit.EntrySchedule, targetOf(content.RefEntry, draftEN))
				w.changed, w.before, w.revision, w.status = false, seqOf(first.Version.Seq), rev, "scheduled"
				w.detail = map[string]string{"at": "2026-11-07T11:00:00Z"}
				return w
			},
		}
	})
	add("Save of the same bytes, with an operation ID", func(t *testing.T, ed *Editor, s *store.Store) step {
		cur := read(t, s, content.RefTerm, newsID)
		withID := Op{By: userID, ID: testOpID(4)}
		return step{
			call: func() (Result, error) {
				return ed.Save(t.Context(), withID, content.RefTerm, newsID, cur.Seq, cur.Data)
			},
			want: func(res Result) wantRecord {
				w := newWant(audit.TermSave, targetOf(content.RefTerm, newsID))
				w.changed, w.operation, w.before = false, withID.ID, seqOf(cur.Seq)
				return w
			},
		}
	})
	return cases
}

// TestAuditRecords checks the record that each operation adds to the activity log when it succeeds, whether or not it
// saves anything: one, in the Update that saves, which points at what the operation did and copies none of the documents.
func TestAuditRecords(t *testing.T) {
	seen := make(map[audit.Action]bool)
	for _, group := range []struct {
		name  string
		cases []auditCase
	}{{"saves", auditSaved()}, {"saves nothing", auditNoOps()}} {
		t.Run(group.name, func(t *testing.T) {
			for _, tt := range group.cases {
				t.Run(tt.name, func(t *testing.T) {
					s, st := tt.prepare(t)
					want := runStep(t, s, st)
					seen[want.action] = true
				})
			}
		})
	}
	for _, action := range editActions {
		if !seen[action] {
			t.Errorf("no test records the action %s", action)
		}
	}
}

// TestAuditFailedOperations checks that an operation that fails adds nothing to the activity log: no record and no
// document, whatever the reason.
func TestAuditFailedOperations(t *testing.T) {
	op := Op{By: userID}
	for _, tt := range []struct {
		name    string
		prepare func(t *testing.T, ed *Editor, s *store.Store) func() error
		is      error
	}{
		{"a document with a problem", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			bad := replaceOnce(t, redirectJSON("/old-contact/", "/contact/"), `"/old-contact/"`, `"old-contact"`)
			return func() error { _, err := ed.Create(t.Context(), op, content.RefRedirect, []byte(bad)); return err }
		}, nil},
		{"a version that is not the current one", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			cur := read(t, s, content.RefTerm, newsID)
			return func() error {
				_, err := ed.Save(t.Context(), op, content.RefTerm, newsID, cur.Seq+1, cur.Data)
				return err
			}
		}, ErrConflict},
		{"a document that does not exist", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error {
				_, err := ed.Save(t.Context(), op, content.RefTerm, testID(content.RefTerm, 9), 1, []byte(termJSON("tag", "ja", "", "a", "a")))
				return err
			}
		}, fs.ErrNotExist},
		{"a document that published content uses", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error {
				_, err := ed.Delete(t.Context(), op, content.RefMedia, jpegMedium, read(t, s, content.RefMedia, jpegMedium).Seq)
				return err
			}
		}, nil},
		{"an entry that is not scheduled", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error { _, err := ed.PublishScheduled(t.Context(), draftEN, careersAt); return err }
		}, ErrNotDue},
		{"an entry that is not due", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			scheduleCareers(t, ed, s)
			return func() error { _, err := ed.PublishScheduled(t.Context(), draftEN, testNow); return err }
		}, ErrNotDue},
		{"an operation ID used for another request", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			withID := Op{By: userID, ID: testOpID(5)}
			must(t)(ed.Create(t.Context(), withID, content.RefRedirect, []byte(redirectJSON("/old-contact/", "/contact/"))))
			return func() error {
				_, err := ed.Create(t.Context(), withID, content.RefRedirect, []byte(redirectJSON("/old-contact/", "/about/")))
				return err
			}
		}, nil},
		{"a revision that is not the latest", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error {
				_, err := ed.SaveRevision(t.Context(), op, draftEN, testID(content.RefRevision, 1), []byte(careersRevision))
				return err
			}
		}, ErrConflict},
		{"an unknown type of entry", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error { _, err := ed.CreateEntry(t.Context(), op, "nothing", "ja"); return err }
		}, nil},
		{"a version of an entry that is not the current one", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
			seq := entrySeq(t, s, draftEN)
			return func() error { _, err := ed.Publish(t.Context(), op, draftEN, seq+1, rev); return err }
		}, ErrConflict},
		{"a time that has passed", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
			seq := entrySeq(t, s, draftEN)
			return func() error {
				_, err := ed.Schedule(t.Context(), op, draftEN, seq, rev, testNow.Add(-time.Hour))
				return err
			}
		}, nil},
		{"a file of a format that is not supported", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error {
				_, err := ed.AddMedia(t.Context(), op, strings.NewReader("not an image"), "a.png", "")
				return err
			}
		}, nil},
		{"a file for a medium that does not exist", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error {
				_, err := ed.ReplaceMedia(t.Context(), op, testID(content.RefMedia, 9), 1, bytes.NewReader(testPNG(t, 5, 4, 30)), "")
				return err
			}
		}, fs.ErrNotExist},
		{"settings that do not validate", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			cur := read(t, s, store.SettingsKind, "")
			bad := replaceOnce(t, string(cur.Data), `"Asia/Tokyo"`, `"Nowhere/City"`)
			return func() error { _, err := ed.SaveSettings(t.Context(), op, cur.Seq, []byte(bad)); return err }
		}, nil},
		{"a user that is not one", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error {
				_, err := ed.Create(t.Context(), Op{By: "usr_x"}, content.RefRedirect, []byte(redirectJSON("/old-contact/", "/contact/")))
				return err
			}
		}, nil},
		{"a store that has documents", func(t *testing.T, ed *Editor, s *store.Store) func() error {
			return func() error { _, err := Import(t.Context(), s, openSample(t), userID); return err }
		}, errNotEmpty},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			call := tt.prepare(t, ed, s)
			records, snap := activity(t, s), snapshot(t, s)
			err := call()
			if err == nil || tt.is != nil && !errors.Is(err, tt.is) {
				t.Fatalf("the operation = %v, want an error that wraps %v", err, tt.is)
			}
			if got := activity(t, s); !reflect.DeepEqual(got, records) {
				t.Errorf("the operation that failed added %d records to the activity log", len(got)-len(records))
			}
			if got := snapshot(t, s); got != snap {
				t.Errorf("the operation that failed has changed the store:\n%s\nwant\n%s", got, snap)
			}
			verifyChains(t, s)
		})
	}
}

// TestAuditRetry checks that a retry of an operation with an operation ID, which returns the recorded result, adds no
// record, whether the operation saved something or not.
func TestAuditRetry(t *testing.T) {
	ed, s := newSample(t)
	data := redirectJSON("/old-contact/", "/contact/")
	created := Op{By: userID, ID: testOpID(6)}
	records := len(activity(t, s))
	first := must(t)(ed.Create(t.Context(), created, content.RefRedirect, []byte(data)))
	if got := len(activity(t, s)); got != records+1 {
		t.Fatalf("Create() added %d records, want 1", got-records)
	}
	same := Op{By: userID, ID: testOpID(7)}
	cur := read(t, s, content.RefTerm, newsID)
	noop := must(t)(ed.Save(t.Context(), same, content.RefTerm, newsID, cur.Seq, cur.Data))
	if got := len(activity(t, s)); got != records+2 {
		t.Fatalf("Save() of the same bytes added %d records, want 1", got-records-1)
	}

	for _, tt := range []struct {
		name string
		call func() (Result, error)
		want Result
	}{
		{"Create", func() (Result, error) {
			return ed.Create(t.Context(), created, content.RefRedirect, []byte(data))
		}, first},
		{"Save of the same bytes", func() (Result, error) {
			return ed.Save(t.Context(), same, content.RefTerm, newsID, cur.Seq, cur.Data)
		}, noop},
	} {
		for range 2 {
			res, err := tt.call()
			if err != nil || !reflect.DeepEqual(res, tt.want) {
				t.Errorf("a retry of %s = %+v, %v; want the first result %+v", tt.name, res, err, tt.want)
			}
		}
	}
	if got := len(activity(t, s)); got != records+2 {
		t.Errorf("the retries added %d records to the activity log, want none", got-records-2)
	}

	// An operation ID that another request has used fails, and adds no record either.
	_, err := ed.Delete(t.Context(), created, content.RefRedirect, first.ID, first.Version.Seq)
	if want := fmt.Sprintf("operation ID %q was used for another request", created.ID); err == nil || err.Error() != want {
		t.Errorf("Delete() with the operation ID of Create() = %v, want the error %q", err, want)
	}
	if got := len(activity(t, s)); got != records+2 {
		t.Errorf("the operation ID used for another request added %d records to the activity log, want none", got-records-2)
	}
	verifyChains(t, s)
}

// TestAuditOperationRecord checks that the audit fields of a request do not change its hash, which the record of the
// operation ID keeps: the request of each operation that has a base is the hash of its name, parameters and data alone.
func TestAuditOperationRecord(t *testing.T) {
	ed, s := newSample(t)
	data := termJSON("tag", "ja", "", "お祭り2", "festival2")
	created := Op{By: userID, ID: testOpID(8)}
	made := must(t)(ed.Create(t.Context(), created, content.RefTerm, []byte(data)))
	saved := Op{By: userID, ID: testOpID(9)}
	changed := replaceOnce(t, string(read(t, s, content.RefTerm, made.ID).Data), `"お祭り2"`, `"お祭り3"`)
	must(t)(ed.Save(t.Context(), saved, content.RefTerm, made.ID, made.Version.Seq, []byte(changed)))
	deleted := Op{By: userID, ID: testOpID(10)}
	seq := read(t, s, content.RefTerm, made.ID).Seq
	must(t)(ed.Delete(t.Context(), deleted, content.RefTerm, made.ID, seq))

	for _, tt := range []struct {
		id string
		q  request
	}{
		{created.ID, request{name: "create", params: []string{"term"}, data: []byte(data)}},
		{saved.ID, request{name: "save", params: []string{"term", made.ID, strconv.FormatInt(made.Version.Seq, 10)}, data: []byte(changed)}},
		{deleted.ID, request{name: "delete", params: []string{"term", made.ID, strconv.FormatInt(seq, 10)}}},
	} {
		err := s.View(t.Context(), func(tx *store.Tx) error {
			stored, ok, err := tx.Operation(tt.id)
			if err != nil || !ok {
				t.Errorf("Operation(%s) = %s, %t, %v; want its record", tt.id, stored, ok, err)
				return nil
			}
			rec, err := decodeRecord(tt.id, stored)
			if err != nil {
				t.Error(err)
			} else if want := tt.q.hash(userID); rec.Request != want {
				t.Errorf("the record of %s has the request %s, want the hash %s of %q, its parameters and its data", tt.id, rec.Request, want, tt.q.name)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	base := request{name: "save", params: []string{"term", "trm_a", "1"}, data: []byte("data")}
	audited := base
	audited.action, audited.base, audited.noVersion = audit.TermSave, 1, true
	if base.hash(userID) != audited.hash(userID) {
		t.Error("the audit fields of a request change its hash")
	}
}

// TestAuditSameUpdate checks that the record of an operation and what the operation saves are in one Update: if the record
// is refused, nothing that the operation saved stays; if the operation fails, no record does.
func TestAuditSameUpdate(t *testing.T) {
	data := redirectJSON("/old-contact/", "/contact/")
	save := func(id string, fail error) func(r *run) (Result, error) {
		return func(r *run) (Result, error) {
			v, err := r.tx.Put(content.RefRedirect, id, []byte(data), userID)
			if err != nil {
				return Result{}, err
			}
			return Result{Kind: content.RefRedirect, ID: id, Version: v}, fail
		}
	}
	id := testID(content.RefRedirect, 20)

	for _, tt := range []struct {
		name   string
		q      request
		fail   error
		stays  bool
		errMsg string
	}{
		{"both", request{name: "x", action: audit.RedirectCreate}, nil, true, ""},
		{"a record that is refused", request{name: "x", action: "nothing.create"}, nil, false, "audit record: action: unknown action"},
		{"an operation that fails", request{name: "x", action: audit.RedirectCreate}, errors.New("failed after the write"), false, "failed after the write"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			records, snap := activity(t, s), snapshot(t, s)
			_, err := ed.run(t.Context(), Op{By: userID}, tt.q, save(id, tt.fail))
			if tt.errMsg == "" && err != nil || tt.errMsg != "" && (err == nil || err.Error() != tt.errMsg) {
				t.Fatalf("run() = %v, want the error %q", err, tt.errMsg)
			}
			stored, recorded := false, len(activity(t, s)) > len(records)
			err = s.View(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Document(content.RefRedirect, id)
				stored = err == nil
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if stored != tt.stays || recorded != tt.stays {
				t.Errorf("the document is stored: %t, the record is added: %t; want both %t", stored, recorded, tt.stays)
			}
			if !tt.stays && snapshot(t, s) != snap {
				t.Error("the store has changed")
			}
			verifyChains(t, s)
		})
	}
}

// TestKindActions checks that the actions of Create, Save and Delete are those of the kinds that the operations handle,
// and that each is an action of the activity log.
func TestKindActions(t *testing.T) {
	kinds := []content.RefKind{content.RefEntry}
	for kind := range docKinds {
		kinds = append(kinds, kind)
	}
	for _, kind := range kinds {
		a, ok := actionsOf[kind]
		if !ok {
			t.Errorf("no actions for %s", kind)
			continue
		}
		// Entries are made and changed by their own operations, and media are added with their files.
		for _, c := range []struct {
			op     string
			action audit.Action
			has    bool
		}{
			{"Create", a.create, kind != content.RefEntry && kind != content.RefMedia},
			{"Save", a.save, kind != content.RefEntry},
			{"Delete", a.remove, true},
		} {
			if c.has != (c.action != "") {
				t.Errorf("%s of %s has the action %q, want one: %t", c.op, kind, c.action, c.has)
			}
			if c.action == "" {
				continue
			}
			if !strings.HasPrefix(string(c.action), string(kind)+".") {
				t.Errorf("%s of %s has the action %q, want one of the kind", c.op, kind, c.action)
			}
			if err := (audit.Record{Log: audit.ActivityLog, Action: c.action}).Check(); err != nil {
				t.Errorf("%s of %s has the action %q: %v", c.op, kind, c.action, err)
			}
		}
	}
}

// TestAuditConcurrent checks the log of operations from several goroutines: every one adds its record, and the chain is
// as the store wrote it.
func TestAuditConcurrent(t *testing.T) {
	ed, s := newSample(t)
	records := len(activity(t, s))
	const goroutines, each = 8, 5
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				from := fmt.Sprintf("/c-%d-%d/", g, i)
				if _, err := ed.Create(t.Context(), Op{By: userID}, content.RefRedirect, []byte(redirectJSON(from, "/contact/"))); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	all := activity(t, s)
	if len(all) != records+goroutines*each {
		t.Errorf("the activity log has %d records, want %d", len(all), records+goroutines*each)
	}
	if !slices.IsSortedFunc(all, func(a, b audit.Record) int { return int(a.N) - int(b.N) }) {
		t.Error("the records are not in the order of their numbers")
	}
	verifyChains(t, s)
}
