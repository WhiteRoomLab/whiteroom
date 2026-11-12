package admin

import (
	"io/fs"
	"sync"
	"testing"
	"time"
)

// buildGate holds the builds of a Publisher whose theme it wraps (gatedFS): while it holds them, a build waits in its first
// open of a file of the theme, with the output directory locked, until the test lets it go.
type buildGate struct {
	mu      sync.Mutex
	hold    chan struct{} // closed to let the builds go; nil while they are free
	reached chan struct{} // gets a value when a build is held
}

func newBuildGate() *buildGate { return &buildGate{reached: make(chan struct{}, 1)} }

func (g *buildGate) pass() {
	g.mu.Lock()
	hold := g.hold
	g.mu.Unlock()
	if hold == nil {
		return
	}
	select {
	case g.reached <- struct{}{}:
	default:
	}
	<-hold
}

// holdBuilds makes the builds that open a file of the theme from now on wait.
func (g *buildGate) holdBuilds() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hold == nil {
		g.hold = make(chan struct{})
	}
	select {
	case <-g.reached:
	default:
	}
}

// letGo lets the builds that wait go, and the next ones pass.
func (g *buildGate) letGo() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hold != nil {
		close(g.hold)
		g.hold = nil
	}
}

// waitHeld waits until a build is held.
func (g *buildGate) waitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a build to be held")
	}
}

// gatedFS is a file system whose Open waits for the gate. It has no other method than Open, so that every read goes through it.
type gatedFS struct {
	fs.FS
	gate *buildGate
}

func (f gatedFS) Open(name string) (fs.File, error) {
	f.gate.pass()
	return f.FS.Open(name)
}
