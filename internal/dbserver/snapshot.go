package dbserver

import "github.com/WhiteRoomLab/whiteroom/internal/logdb"

// snapshot is a View of the engine that stays open between the requests of a session. A *logdb.Tx is valid only inside
// the function of View, so the session serves its requests inside that function while it holds the snapshot. The View
// holds no lock while its function runs, and a Commit of the session runs its Update inside it.
type snapshot struct {
	seq uint64
	tx  *logdb.Tx
}

// openSnapshot fixes the state of the store as it is now and calls held with it; the snapshot ends when held returns. It
// does not wait for a commit, nor does a commit wait for it. It reports whether held was called.
func (s *Server) openSnapshot(held func(sn *snapshot)) (called bool, err error) {
	err = s.db.View(s.ctx, func(tx *logdb.Tx) error {
		called = true
		held(&snapshot{seq: tx.Seq(), tx: tx}) // the first read fixes the state
		return nil
	})
	return called, err
}

func (sn *snapshot) do(f func(tx *logdb.Tx)) { f(sn.tx) }
