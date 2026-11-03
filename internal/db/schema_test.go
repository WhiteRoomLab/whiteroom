package db

import (
	"database/sql"
	"slices"
	"strings"
	"testing"
)

// consistency counts the documents whose row in documents is not their latest version: a row whose version is not
// the latest, a row although the latest version is a deletion, or no row although it is not.
const consistency = `SELECT count(*) FROM (SELECT kind, id, max(seq) AS seq FROM versions GROUP BY kind, id) AS latest
JOIN versions AS v ON v.seq = latest.seq
LEFT JOIN documents AS d ON d.kind = latest.kind AND d.id = latest.id
WHERE (v.object IS NULL) != (d.version IS NULL) OR d.version != latest.seq`

func TestTables(t *testing.T) {
	const (
		entryID    = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5"
		pageID     = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"
		termID     = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8"
		revisionID = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9"
		otherRevID = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W8"
		saved      = "'2026-11-03T08:00:00.000000000Z', 'usr_01JB8T7H4MA2C5E8G1J4K7N3P6'" // saved_at, saved_by

		object   = "INTO objects (hash, data) VALUES (?, x'03')"
		version  = "INSERT INTO versions (kind, id, object, saved_at, saved_by) VALUES (?, ?, ?, " + saved + ")"
		document = "INSERT INTO documents (kind, id, version) VALUES (?, ?, ?)"
		upsert   = document + " ON CONFLICT (kind, id) DO UPDATE SET version = excluded.version"
		revision = "INSERT INTO revisions (id, entry, object, saved_at, saved_by) VALUES (?, ?, ?, " + saved + ")"
	)
	h1 := "sha256:" + strings.Repeat("1", 64)
	h2 := "sha256:" + strings.Repeat("2", 64)
	missing := "sha256:" + strings.Repeat("3", 64)
	d, _ := newDB(t)
	// The versions that are added get the seq 1 to 7 in their order.
	for _, tt := range []struct {
		name  string
		query string
		args  []any
		want  string // the error; "" for none
	}{
		{"objects", "INSERT INTO objects (hash, data) VALUES (?, x'01'), (?, x'02')", []any{h1, h2}, ""},
		{"same object", "INSERT " + object, []any{h1}, "UNIQUE constraint failed: objects.hash"},
		{"same object ignored", "INSERT OR IGNORE " + object, []any{h1}, ""},
		{"object replaced", "INSERT OR REPLACE " + object, []any{h1}, "objects are immutable"},
		{"object updated", "UPDATE objects SET data = x'03' WHERE hash = ?", []any{h1}, "objects are immutable"},
		{"object deleted", "DELETE FROM objects WHERE hash = ?", []any{h1}, "objects are immutable"},

		{"version", version, []any{"entry", entryID, h1}, ""},
		{"deletion", version, []any{"entry", entryID, nil}, ""},
		{"version again", version, []any{"entry", entryID, h2}, ""},
		{"version of a term", version, []any{"term", termID, h1}, ""},
		{"settings", version, []any{"settings", "", h2}, ""},
		{"version of another entry", version, []any{"entry", pageID, h1}, ""},
		{"deletion of another entry", version, []any{"entry", pageID, nil}, ""},
		{"unknown kind", version, []any{"page", entryID, h1}, "CHECK constraint failed: kind"},
		{"settings with an ID", version, []any{"settings", entryID, h1}, "CHECK constraint failed: settings_id"},
		{"entry without an ID", version, []any{"entry", "", h1}, "CHECK constraint failed: settings_id"},
		{"missing object", version, []any{"entry", entryID, missing}, "FOREIGN KEY constraint failed"},
		{
			"version replaced",
			"INSERT OR REPLACE INTO versions (seq, kind, id, object, saved_at, saved_by) VALUES (1, 'entry', ?, NULL, " +
				saved + ")",
			[]any{entryID},
			"versions are immutable",
		},
		{"version updated", "UPDATE versions SET object = NULL WHERE seq = 1", nil, "versions are immutable"},
		{"version deleted", "DELETE FROM versions WHERE seq = 1", nil, "versions are immutable"},

		{"deletion inserted as current", document, []any{"entry", entryID, 2}, "a deletion cannot be the current version"},
		{"current", document, []any{"entry", entryID, 1}, ""},
		{"deletion upserted as current", upsert, []any{"entry", entryID, 2}, "a deletion cannot be the current version"},
		{
			"deletion updated as current",
			"UPDATE documents SET version = 2 WHERE kind = 'entry' AND id = ?",
			[]any{entryID},
			"a deletion cannot be the current version",
		},
		{
			"version of another document",
			"UPDATE documents SET version = 4 WHERE kind = 'entry' AND id = ?",
			[]any{entryID},
			"FOREIGN KEY constraint failed",
		},
		{"missing version", document, []any{"term", termID, 99}, "FOREIGN KEY constraint failed"},
		{"current upserted", upsert, []any{"entry", entryID, 3}, ""},
		{"current term", document, []any{"term", termID, 4}, ""},
		{"current settings", document, []any{"settings", "", 5}, ""},

		{"revision", revision, []any{revisionID, entryID, h1}, ""},
		{"same revision ID", revision, []any{revisionID, entryID, h2}, "UNIQUE constraint failed: revisions.id"},
		{"revision of a missing object", revision, []any{otherRevID, entryID, missing}, "FOREIGN KEY constraint failed"},
		{
			"revision replaced",
			"INSERT OR REPLACE INTO revisions (seq, id, entry, object, saved_at, saved_by) VALUES (1, ?, ?, ?, " +
				saved + ")",
			[]any{revisionID, entryID, h2},
			"revisions are immutable",
		},
		{"revision updated", "UPDATE revisions SET object = ? WHERE seq = 1", []any{h2}, "revisions are immutable"},
		{"revision deleted", "DELETE FROM revisions WHERE seq = 1", nil, "revisions are immutable"},
	} {
		err := d.Write(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(tt.query, tt.args...)
			return err
		})
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tt.want {
			t.Errorf("%s: Write() = %v, want %q", tt.name, err, tt.want)
		}
	}

	if err := d.Read(t.Context(), func(tx *sql.Tx) error {
		for _, tt := range []struct {
			query string
			want  []string
		}{
			{"SELECT hash, hex(data) FROM objects ORDER BY hash", []string{h1 + " 01", h2 + " 02"}},
			{"SELECT count(*) FROM versions", []string{"7"}},
			{
				"SELECT kind, id, version FROM documents ORDER BY kind, id",
				[]string{"entry " + entryID + " 3", "settings  5", "term " + termID + " 4"},
			},
			{"SELECT id, entry, object FROM revisions", []string{revisionID + " " + entryID + " " + h1}},
			{"PRAGMA foreign_key_check", nil},
			{"PRAGMA integrity_check", []string{"ok"}},
			{consistency, []string{"0"}},
		} {
			if got, err := rows(tx, tt.query); err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("rows of %q = %q, %v; want %q", tt.query, got, err, tt.want)
			}
		}
		for _, tt := range []struct {
			query string
			args  []any
			index string
		}{
			{
				"SELECT seq FROM versions WHERE kind = ? AND id = ? ORDER BY seq",
				[]any{"entry", entryID},
				"sqlite_autoindex_versions_1",
			},
			{"SELECT seq FROM revisions WHERE entry = ? ORDER BY seq", []any{entryID}, "revisions_by_entry"},
		} {
			plan, err := rows(tx, "EXPLAIN QUERY PLAN "+tt.query, tt.args...)
			if err != nil || !slices.ContainsFunc(plan, func(s string) bool { return strings.Contains(s, tt.index) }) ||
				slices.ContainsFunc(plan, func(s string) bool { return strings.Contains(s, "TEMP B-TREE") }) {
				t.Errorf("plan of %q = %q, %v; want %s without a temporary B-tree", tt.query, plan, err, tt.index)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("Read() = %v", err)
	}
}
