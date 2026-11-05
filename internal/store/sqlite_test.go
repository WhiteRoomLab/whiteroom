package store

import (
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/db"
)

// sqliteDB returns the database of s, a store on SQLite.
func sqliteDB(t *testing.T, s *Store) *db.DB {
	t.Helper()
	b, ok := s.b.(*sqliteBackend)
	if !ok {
		t.Fatalf("the backend of the store is %T, want SQLite", s.b)
	}
	return b.db
}
