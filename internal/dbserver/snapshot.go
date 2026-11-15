package dbserver

import (
	"errors"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// snapshot is a View of the engine that stays open between the requests of a session. A *logdb.Tx is valid only inside
// the function of View and is not for concurrent use, so a goroutine runs View, and the session sends it one function
// per request and waits for it.
type snapshot struct {
	seq   uint64
	calls chan func(*logdb.Tx)
	ack   chan struct{}
	done  chan struct{} // closed when View has returned
}

// openSnapshot fixes the state of the store as it is now. It does not wait for a commit, nor does a commit wait for it.
func (s *Server) openSnapshot() (*snapshot, error) {
	sn := &snapshot{calls: make(chan func(*logdb.Tx)), ack: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan struct{})
	var err error
	go func() {
		defer close(sn.done)
		err = s.db.View(s.ctx, func(tx *logdb.Tx) error {
			sn.seq = tx.Seq() // the first read fixes the state
			close(ready)
			for f := range sn.calls {
				f(tx)
				sn.ack <- struct{}{}
			}
			return nil
		})
	}()
	select {
	case <-ready:
		return sn, nil
	case <-sn.done:
		if err == nil {
			err = errors.New("the snapshot did not start")
		}
		return nil, err
	}
}

func (sn *snapshot) do(f func(tx *logdb.Tx)) {
	sn.calls <- f
	<-sn.ack
}

func (sn *snapshot) release() {
	close(sn.calls)
	<-sn.done
}
