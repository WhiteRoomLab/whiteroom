// Package serve is the built-in public server (9.5), which serves the releases that the build publishes.
package serve

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Handler serves the public directory of the release that out/current points to, with the redirects, the 404 pages
// and the directory index of its server.json. It reads out/current for every request, so that a new build is served
// from the next request, server.json included.
type Handler struct {
	out *os.Root
	log *log.Logger

	mu  sync.Mutex
	rel *release // the last one loaded
}

// New returns a Handler for out, the output directory of builds. It fails if out has no published release. errorLog
// gets the errors that requests run into, such as an invalid server.json; nil discards them.
func New(out string, errorLog *log.Logger) (*Handler, error) {
	abs, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	if errorLog == nil {
		errorLog = log.New(io.Discard, "", 0)
	}
	h := &Handler{out: root, log: errorLog}
	if _, err := h.current(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return h, nil
}

// Base returns the path of the site URL of the published release in the canonical form of froms, such as "/blog"; ""
// at the root of its host.
func (h *Handler) Base() (string, error) {
	rel, err := h.current()
	if err != nil {
		return "", err
	}
	return rel.base, nil
}

// Close closes out.
func (h *Handler) Close() error {
	return h.out.Close()
}
