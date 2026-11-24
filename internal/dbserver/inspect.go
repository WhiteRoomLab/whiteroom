package dbserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The areas of Problem.
const (
	AreaEngine   = "engine"   // the files of the storage engine
	AreaOriginal = "original" // the originals in files/
)

// Problem is one thing that Inspect finds wrong in a data directory. Target names the file, "" if the problem is not about
// one. No field holds a key or a value of a row, or data of an object or an original.
type Problem struct {
	Area   string
	Target string
	Detail string
}

var oneLine = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// String returns the problem on one line: the area, the target if it has one, and the detail.
func (p Problem) String() string {
	if p.Target == "" {
		return oneLine.Replace(p.Area + ": " + p.Detail)
	}
	return oneLine.Replace(p.Area + ": " + p.Target + ": " + p.Detail)
}

// Inspect looks for what is wrong in the data directory dir, which no daemon or other process has open, and calls found
// with each problem, until found returns false, which stops it: Inspect returns nil then. It changes nothing: the engine
// runs through logdb.Inspect on a file system that refuses every writing call, and the mark of a failed sync is read and
// never removed. It reports what logdb.Inspection.Check does of the engine (also a layout that the engine does not open),
// and then every original in files/ whose data does not have the hash that its name says, or that is not a regular file.
//
// It waits for the processes that have the directory open for up to two seconds, and then fails with an error that wraps
// ErrInUse. It refuses, as Maintain does, a directory whose mark of a failed sync is of the boot of the machine
// (ErrNotRestarted) and one whose mark cannot be read; a mark of an earlier boot is left where it is. Unlike Maintain,
// it does not need to write in the directory, and does not call o.Log.Accept. It returns an error for the end of ctx, and
// for what stops it from looking.
func Inspect(ctx context.Context, dir string, o Options, found func(Problem) bool) error {
	root, _, err := dataRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return inspect(ctx, root, dir, o, found)
}

// inspect is Inspect on the data directory root, which stays the caller's; dir is its name in errors.
func inspect(ctx context.Context, root vfs.FS, dir string, o Options, found func(Problem) bool) error {
	boot, err := bootOf(o)
	if err != nil {
		return err
	}
	for _, name := range []string{"logdb", "files"} {
		if err := checkDir(root, dir, name); err != nil {
			return err
		}
	}
	if _, err := checkMark(root, dir, boot); err != nil {
		return err
	}
	logSub, err := root.Sub("logdb")
	if err != nil {
		return err
	}
	defer func() { _ = logSub.Close() }()
	filesSub, err := root.Sub("files")
	if err != nil {
		return err
	}
	defer func() { _ = filesSub.Close() }()
	frozen := new(atomic.Bool)
	frozen.Store(true)
	logFS := &writeFS{FS: logSub, to: new(atomic.Pointer[atomic.Uint64]), frozen: frozen}
	files := &writeFS{FS: filesSub, to: new(atomic.Pointer[atomic.Uint64]), frozen: frozen}

	stopped := false
	report := func(p Problem) bool {
		if !found(p) {
			stopped = true
		}
		return !stopped
	}

	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(lockWait, cancel)
	defer timer.Stop()
	entered := false
	var refused error
	err = logdb.Inspect(waitCtx, logFS, o.Log, func(in *logdb.Inspection) error {
		entered = true
		timer.Stop()
		// A process that stopped while this one waited for the locks has left its mark since the first look: what the page
		// cache has, and the disk may not, is not what this reads.
		if _, err := checkMark(root, dir, boot); err != nil {
			refused = err
			return err
		}
		if err := in.Check(ctx, func(p logdb.Problem) bool { return report(engineProblem(p)) }); err != nil || stopped {
			return err
		}
		return checkOriginals(ctx, files, report)
	})
	switch {
	case refused != nil:
		return refused
	case err == nil:
		return nil
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		return err
	case !entered && errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", dir, ErrInUse)
	case !entered:
		// What the engine refuses before it takes its locks: a file of its layout that is missing or is not what it must be.
		report(Problem{Area: AreaEngine, Detail: err.Error()})
		return nil
	}
	return err
}

func engineProblem(p logdb.Problem) Problem {
	target := p.Path
	if p.Offset >= 0 {
		if target != "" {
			target += " "
		}
		target += "at offset " + strconv.FormatInt(p.Offset, 10)
	}
	return Problem{Area: AreaEngine, Target: target, Detail: p.Reason}
}

// checkOriginals looks at each file of files/ that has the name of an original, files/<2 hex digits>/<64 hex digits> with
// the first two of the 64 in the name of the directory, in the order of their names; it looks at no other name.
func checkOriginals(ctx context.Context, files vfs.FS, report func(Problem) bool) error {
	tops, err := files.ReadDir(".")
	if err != nil {
		report(Problem{Area: AreaOriginal, Target: "files", Detail: "cannot read: " + innerText(err)})
		return nil
	}
	for _, top := range tops {
		dir := top.Name()
		if !top.IsDir() || !isHexName(dir, 2) {
			continue
		}
		entries, err := files.ReadDir(dir)
		if err != nil {
			if !report(Problem{Area: AreaOriginal, Target: "files/" + dir, Detail: "cannot read: " + innerText(err)}) {
				return nil
			}
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !isHexName(name, 64) || name[:2] != dir {
				continue
			}
			detail, err := checkOriginal(ctx, files, dir+"/"+name, name)
			if err != nil {
				return err
			}
			if detail != "" && !report(Problem{Area: AreaOriginal, Target: "files/" + dir + "/" + name, Detail: detail}) {
				return nil
			}
		}
	}
	return nil
}

// checkOriginal reads the file name in files/, whose data must have the hash digits. It returns what is wrong with it, "" if
// nothing is, and an error only for the end of ctx.
func checkOriginal(ctx context.Context, files vfs.FS, name, digits string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Lstat before opening: opening a named pipe would wait for a writer.
	info, err := files.Lstat(name)
	switch {
	case err != nil:
		return "cannot read: " + innerText(err), nil
	case info.Mode()&fs.ModeSymlink != 0:
		return "symbolic links are not allowed", nil
	case !info.Mode().IsRegular():
		return "must be a regular file", nil
	}
	f, err := files.Open(name)
	if err != nil {
		return "cannot read: " + innerText(err), nil
	}
	defer func() { _ = f.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, ctxReader{ctx, f}); err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return "", err
		}
		return "cannot read: " + innerText(err), nil
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != digits {
		return "the data has the hash sha256:" + got, nil
	}
	return "", nil
}

// isHexName reports whether name has n lower case hex digits and nothing else.
func isHexName(name string, n int) bool {
	if len(name) != n {
		return false
	}
	for i := range len(name) {
		if strings.IndexByte("0123456789abcdef", name[i]) < 0 {
			return false
		}
	}
	return true
}

// ctxReader reads from r unless ctx has ended.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
