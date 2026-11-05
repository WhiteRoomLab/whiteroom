// Package logdb is the storage engine of the Content Store (internal/store; concept section 3.5). One append-only log
// of commit records is the source of truth; objects are content-addressed; the tables are persistent maps in memory,
// rebuilt from the log and from checkpoints. It uses the standard library only.
package logdb
