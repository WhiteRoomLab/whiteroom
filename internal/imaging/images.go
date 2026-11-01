package imaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Options are the options of New.
type Options struct {
	Dir string // the cache directory; see New and Images
	// Memory is about how many bytes the jobs of Files may take at once at most, as estimated: the originals, their
	// decoded images and the files that they make. 0 is 1 GiB. A job that takes more runs alone.
	Memory int64
}

const (
	defaultMemory = 1 << 30
	cacheTagName  = "CACHEDIR.TAG"
	// cacheTag marks the cache directory as one of whiteroom, so that New takes no other directory for it, whose files
	// Prune would remove, and as a cache, which backup programs may skip (https://bford.info/cachedir/).
	cacheTag = "Signature: 8a477f597d28d172789f06886806bc55\n" +
		"# This file is a cache directory tag created by whiteroom.\n" +
		"# For information about cache directory tags, see https://bford.info/cachedir/\n"
)

// versionDir is the directory of the cache of processingVersion in the cache directory.
var versionDir = "v" + strconv.Itoa(processingVersion)

// Images makes the files of image media from their originals and caches them in a directory, with what Inspect reads
// from the originals, by the hashes of the originals: what Info returns in v<version>/info/<2 hex digits>/<64 hex
// digits of the hash>.json, and each file in v<version>/files/<2 hex digits>/<cache key><extension>. The files are the
// same bytes whether they come from the cache or not.
//
// It is safe for concurrent use; the calls for the same original wait for each other. Its directory must be used by
// one Images at a time: two builds that run at the same time need two directories, as Prune of each would remove the
// files of the other.
type Images struct {
	dir    string
	budget *budget
	inUse  sync.RWMutex // read-locked by Info and Files, and locked by Prune
	mu     sync.Mutex   // of the fields below
	used   map[string]bool
	infos  map[string]Info // by hash
	locks  map[string]*hashLock
	// jobHook is called, if not nil, by each job of Files while it holds the budget, and done when it ends; for tests.
	jobHook func(hash string, vs []Variant, weight int64) (done func())
}

// hashLock is held by the call of Info or Files that reads or makes what the cache does not have of an original.
type hashLock struct {
	held chan struct{} // has a value while the lock is held
	refs int           // the calls that hold it or wait for it
}

// New returns the Images of opts. It creates the cache directory with a CACHEDIR.TAG file, or opens one that it has
// created; a directory that is not empty without that file is an error.
func New(opts Options) (*Images, error) {
	switch {
	case opts.Dir == "":
		return nil, errors.New("no cache directory")
	case opts.Memory < 0:
		return nil, fmt.Errorf("memory %d is out of range (0 or more)", opts.Memory)
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}
	if err := openCacheDir(dir); err != nil {
		return nil, err
	}
	memory := opts.Memory
	if memory == 0 {
		memory = defaultMemory
	}
	return &Images{
		dir:    dir,
		budget: newBudget(memory),
		used:   map[string]bool{cacheTagName: true},
		infos:  make(map[string]Info),
		locks:  make(map[string]*hashLock),
	}, nil
}

// openCacheDir creates dir with cacheTag in it, or checks that it has it.
func openCacheDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tag, err := os.ReadFile(filepath.Join(dir, cacheTagName))
	switch {
	case err == nil && string(tag) == cacheTag:
		return nil
	case err == nil:
		return fmt.Errorf("%s: a cache directory of another program", dir)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s: not empty, and not a cache directory of whiteroom (no %s)", dir, cacheTagName)
	}
	return writeFile(filepath.Join(dir, cacheTagName), []byte(cacheTag))
}

// Info returns what Inspect reads from the original whose Media.Hash is hash: from the cache, or else from the
// original that open opens, which it checks against hash, and caches it.
func (im *Images) Info(hash string, open func() (io.ReadCloser, error)) (Info, error) {
	digits, err := hashDigits(hash)
	if err != nil {
		return Info{}, err
	}
	im.inUse.RLock()
	defer im.inUse.RUnlock()
	if info, ok := im.knownInfo(hash); ok {
		return info, nil
	}
	unlock, err := im.lock(context.Background(), hash)
	if err != nil {
		return Info{}, err
	}
	defer unlock()
	if info, ok := im.knownInfo(hash); ok {
		return info, nil
	}
	name := im.use(filepath.Join(versionDir, "info", digits[:2], digits+".json"))
	info, ok := readInfo(name)
	if !ok {
		data, err := loadOriginal(hash, open, 0)
		if err != nil {
			return Info{}, fmt.Errorf("%s: %w", hash, err)
		}
		if info, err = Inspect(data); err != nil {
			return Info{}, fmt.Errorf("%s: %w", hash, err)
		}
		b, err := json.Marshal(infoRecord{Go: goVersion, Info: info})
		if err == nil {
			err = writeFile(name, b)
		}
		if err != nil {
			return Info{}, fmt.Errorf("%s: %w", hash, err)
		}
	}
	im.mu.Lock()
	im.infos[hash] = info
	im.mu.Unlock()
	info.Warnings = slices.Clone(info.Warnings)
	return info, nil
}

// knownInfo returns the Info of hash that Info has returned since New.
func (im *Images) knownInfo(hash string) (Info, bool) {
	im.mu.Lock()
	info, ok := im.infos[hash]
	im.mu.Unlock()
	info.Warnings = slices.Clone(info.Warnings)
	return info, ok
}

// infoRecord is an Info in the cache, with the version of Go whose decoders Inspect used.
type infoRecord struct {
	Go   string `json:"go"`
	Info Info   `json:"info"`
}

// readInfo returns the Info in the file name, and false if it is missing, broken or of another version of Go.
func readInfo(name string) (Info, bool) {
	b, err := os.ReadFile(name)
	if err != nil {
		return Info{}, false
	}
	var r infoRecord
	if err := json.Unmarshal(b, &r); err != nil || r.Go != goVersion || r.Info.MIMEType == "" {
		return Info{}, false
	}
	if len(r.Info.Warnings) == 0 {
		r.Info.Warnings = nil // as Inspect returns them
	}
	return r.Info, true
}

// Files returns the files of the variants vs, in their order, of the original whose Media.Hash is hash and whose Info
// is info, as Info returns it. It reads them from the cache, or makes those that the cache does not have from the
// original that open opens, which it checks against hash, within the memory budget, and caches them. It returns
// ctx.Err() if ctx is done before it has made them.
func (im *Images) Files(
	ctx context.Context, hash string, open func() (io.ReadCloser, error), info Info, vs []Variant,
) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := hashDigits(hash); err != nil {
		return nil, err
	}
	im.inUse.RLock()
	defer im.inUse.RUnlock()
	names := make([]string, len(vs))
	all := make([]int, len(vs))
	for i, v := range vs {
		key := cacheKey(hash, v)
		names[i] = im.use(filepath.Join(versionDir, "files", key[:2], key+ext(v.Format)))
		all[i] = i
	}
	files := make([][]byte, len(vs))
	missing := readFiles(files, names, all)
	if missing == nil {
		return files, nil
	}
	unlock, err := im.lock(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Another call may have made them while this one waited.
	if missing = readFiles(files, names, missing); missing == nil {
		return files, nil
	}
	if err := im.makeMissing(ctx, hash, open, info, vs, names, files, missing); err != nil {
		return nil, err
	}
	return files, nil
}

// readFiles reads the files of the cache named at the indexes idx of names into files, and returns the indexes of those
// that are missing, empty or cannot be read.
func readFiles(files [][]byte, names []string, idx []int) []int {
	var missing []int
	for _, i := range idx {
		b, err := os.ReadFile(names[i])
		if err != nil || len(b) == 0 {
			missing = append(missing, i)
			continue
		}
		files[i] = b
	}
	return missing
}

// makeMissing makes the files of vs at the indexes missing into files, from the original, and caches them, in one job.
func (im *Images) makeMissing(
	ctx context.Context, hash string, open func() (io.ReadCloser, error), info Info, vs []Variant, names []string,
	files [][]byte, missing []int,
) error {
	var (
		todo  []Variant
		at    []string               // the names of the files of todo
		index = make(map[string]int) // in todo, by name
	)
	for _, i := range missing {
		if _, ok := index[names[i]]; ok {
			continue
		}
		if err := checkVariant(info, vs[i]); err != nil {
			return fmt.Errorf("%s: %s: %w", hash, vs[i].label(), err)
		}
		index[names[i]] = len(todo)
		todo = append(todo, vs[i])
		at = append(at, names[i])
	}
	n, err := im.budget.acquire(ctx, jobMemory(info, todo))
	if err != nil {
		return err
	}
	defer im.budget.release(n)
	if im.jobHook != nil {
		defer im.jobHook(hash, todo, n)()
	}
	data, err := loadOriginal(hash, open, info.Size)
	if err != nil {
		return fmt.Errorf("%s: %w", hash, err)
	}
	if len(data) != info.Size {
		return fmt.Errorf("%s: an original of %d bytes, not %d as its Info says", hash, len(data), info.Size)
	}
	made, err := makeFiles(ctx, data, info, todo)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("%s: %w", hash, err)
	}
	for k, v := range todo {
		if err := writeFile(at[k], made[k]); err != nil {
			return fmt.Errorf("%s: %s: %w", hash, v.label(), err)
		}
	}
	for _, i := range missing {
		files[i] = made[index[names[i]]]
	}
	return nil
}

// use records the file name, relative to the cache directory, as used, so that Prune keeps it, and returns its path.
func (im *Images) use(name string) string {
	im.mu.Lock()
	im.used[name] = true
	im.mu.Unlock()
	return filepath.Join(im.dir, name)
}

// lock locks the original whose Media.Hash is hash, waiting for the call that holds it, and returns the function that
// unlocks it, or ctx.Err() if ctx is done first.
func (im *Images) lock(ctx context.Context, hash string) (func(), error) {
	im.mu.Lock()
	l := im.locks[hash]
	if l == nil {
		l = &hashLock{held: make(chan struct{}, 1)}
		im.locks[hash] = l
	}
	l.refs++
	im.mu.Unlock()
	leave := func() {
		im.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(im.locks, hash)
		}
		im.mu.Unlock()
	}
	select {
	case l.held <- struct{}{}:
		return func() {
			<-l.held
			leave()
		}, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

// Prune removes from the cache directory every file that Info and Files have not used since New, such as those of
// other versions, of originals and variants that are no longer used, and temporary files, and then the directories
// left empty. A build calls it after it succeeds, when no Info or Files is running; they wait for it.
func (im *Images) Prune() error {
	im.inUse.Lock()
	defer im.inUse.Unlock()
	im.mu.Lock()
	used := maps.Clone(im.used)
	im.mu.Unlock()
	var (
		dirs []string // in the order of the walk, parents first
		errs []error
	)
	err := filepath.WalkDir(im.dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			errs = append(errs, err)
		case d.IsDir():
			if path != im.dir {
				dirs = append(dirs, path)
			}
		default:
			rel, err := filepath.Rel(im.dir, path)
			if err == nil && !used[rel] {
				err = os.Remove(path)
			}
			if err != nil {
				errs = append(errs, err)
			}
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	for _, dir := range slices.Backward(dirs) {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			if err := os.Remove(dir); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// hashDigits returns the hex digits of hash, a Media.Hash: "sha256:" and 64 lower case hex digits.
func hashDigits(hash string) (string, error) {
	d, ok := strings.CutPrefix(hash, "sha256:")
	if !ok || len(d) != 64 || strings.Trim(d, "0123456789abcdef") != "" {
		return "", fmt.Errorf(`invalid hash %q: use "sha256:" and 64 lower case hex digits`, hash)
	}
	return d, nil
}

// loadOriginal reads the original whose Media.Hash is hash, and of about size bytes if size is not 0, from open, and
// checks it against hash.
func loadOriginal(hash string, open func() (io.ReadCloser, error), size int) ([]byte, error) {
	r, err := open()
	if err != nil {
		return nil, err
	}
	data, err := readMedia(r, size)
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != hash {
		return nil, fmt.Errorf("the original has the hash %s", got)
	}
	return data, nil
}

// readMedia reads r to its end into a buffer of size bytes at first, and returns an error after content.MaxMediaBytes.
func readMedia(r io.Reader, size int) ([]byte, error) {
	const limit = content.MaxMediaBytes + 1 // a byte more than an original has, to read the end without growing
	b := make([]byte, 0, min(max(size, 512)+1, limit))
	for {
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		switch {
		case len(b) == limit:
			return nil, fmt.Errorf("the original is larger than %d bytes", content.MaxMediaBytes)
		case errors.Is(err, io.EOF):
			return b, nil
		case err != nil:
			return nil, err
		case len(b) == cap(b):
			b = append(make([]byte, 0, min(2*cap(b), limit)), b...)
		}
	}
}

// writeFile writes data to the file name atomically: to a temporary file in its directory, which it syncs and then
// renames, so that the file is whole or missing, even after a crash.
func writeFile(name string, data []byte) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}
