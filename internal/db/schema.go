package db

// migrations[i] takes the tables from version i to i+1. Append only: never change an entry that main has.
var migrations = []string{
	// 1: the Content Store.
	`
CREATE TABLE objects (
  hash TEXT PRIMARY KEY NOT NULL,
  data BLOB NOT NULL
) STRICT;
CREATE TABLE versions (
  seq      INTEGER PRIMARY KEY,
  kind     TEXT NOT NULL
           CONSTRAINT kind CHECK (kind IN ('entry', 'term', 'media', 'author', 'menu', 'form', 'redirect', 'settings')),
  id       TEXT NOT NULL,
  object   TEXT REFERENCES objects (hash),
  saved_at TEXT NOT NULL,
  saved_by TEXT NOT NULL,
  CONSTRAINT settings_id CHECK ((kind = 'settings') = (id = '')),
  UNIQUE (kind, id, seq) -- the parent key of documents, and the index of the versions of a document
) STRICT;
CREATE TABLE documents (
  kind    TEXT NOT NULL,
  id      TEXT NOT NULL,
  version INTEGER NOT NULL,
  PRIMARY KEY (kind, id),
  FOREIGN KEY (version, kind, id) REFERENCES versions (seq, kind, id)
) STRICT, WITHOUT ROWID;
CREATE TABLE revisions (
  seq      INTEGER PRIMARY KEY,
  id       TEXT NOT NULL UNIQUE,
  entry    TEXT NOT NULL,
  object   TEXT NOT NULL REFERENCES objects (hash),
  saved_at TEXT NOT NULL,
  saved_by TEXT NOT NULL
) STRICT;
CREATE INDEX revisions_by_entry ON revisions (entry, seq);
CREATE TRIGGER objects_no_update BEFORE UPDATE ON objects BEGIN SELECT RAISE(ABORT, 'objects are immutable'); END;
CREATE TRIGGER objects_no_delete BEFORE DELETE ON objects BEGIN SELECT RAISE(ABORT, 'objects are immutable'); END;
CREATE TRIGGER versions_no_update BEFORE UPDATE ON versions BEGIN SELECT RAISE(ABORT, 'versions are immutable'); END;
CREATE TRIGGER versions_no_delete BEFORE DELETE ON versions BEGIN SELECT RAISE(ABORT, 'versions are immutable'); END;
CREATE TRIGGER revisions_no_update BEFORE UPDATE ON revisions BEGIN SELECT RAISE(ABORT, 'revisions are immutable'); END;
CREATE TRIGGER revisions_no_delete BEFORE DELETE ON revisions BEGIN SELECT RAISE(ABORT, 'revisions are immutable'); END;
CREATE TRIGGER documents_no_deletion_insert BEFORE INSERT ON documents
  WHEN EXISTS (SELECT 1 FROM versions WHERE seq = NEW.version AND object IS NULL)
  BEGIN SELECT RAISE(ABORT, 'a deletion cannot be the current version'); END;
CREATE TRIGGER documents_no_deletion_update BEFORE UPDATE ON documents
  WHEN EXISTS (SELECT 1 FROM versions WHERE seq = NEW.version AND object IS NULL)
  BEGIN SELECT RAISE(ABORT, 'a deletion cannot be the current version'); END;
`,
}
