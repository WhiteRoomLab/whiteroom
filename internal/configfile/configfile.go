// Package configfile reads the YAML config file of the commands whiteroom and whiteroom-db (concept section 9.4). One file
// may hold the items of both, so it knows all of them, and only checks their types and ranges.
package configfile

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

const (
	// MaxSize is the most bytes of a config file.
	MaxSize = 1 << 20
	// MaxMemory is the most MiB of memory, whose bytes must fit in an int64.
	MaxMemory int64 = math.MaxInt64 >> 20
)

// Problem is a problem in a config file, at Path, a JSON Pointer to an item ("" for the file as a whole). Line and Column
// start at 1, and Column counts characters; either is 0 if it is not known. Its message does not have the value that the
// file gives.
type Problem struct {
	File   string
	Line   int
	Column int
	Path   string
	Msg    string
}

func (p *Problem) Error() string {
	s := p.Msg
	if p.Path != "" {
		s = p.Path + ": " + s
	}
	var loc []string
	if p.File != "" {
		loc = append(loc, p.File)
	}
	if p.Line > 0 {
		loc = append(loc, strconv.Itoa(p.Line))
		if p.Column > 0 {
			loc = append(loc, strconv.Itoa(p.Column))
		}
	}
	if len(loc) > 0 {
		s = strings.Join(loc, ":") + ": " + s
	}
	return s
}

// Problems lists the problems in a config file, in the order of their positions.
type Problems []*Problem

func (ps Problems) Error() string {
	msgs := make([]string, len(ps))
	for i, p := range ps {
		msgs[i] = p.Error()
	}
	return strings.Join(msgs, "\n")
}

// File holds the items of a config file; those that the file does not give are zero, and Has tells which it gives. The
// paths that the file gives are relative to its directory, so that they are the paths to open from the working
// directory, or absolute.
type File struct {
	Data, Site, Theme, Out, Cache string
	Tmp, DB                       string
	Addr, Socket, Origin          string
	URL                           string // as written: the rule of the URL is checked where it is used
	TrustedProxies                []string
	Workers                       int
	Memory                        int64 // MiB

	WriterUser, WriterGroup, ReaderGroup string // a name, or a number in decimal
	MaxReaders, CommitRate               int
	MinFree, MinFreeInodes, MaxStaged    int64
	SnapshotIdle, Stall                  time.Duration

	items map[string]bool
}

// Has reports whether the file gives the item, such as "commitRate", also with a value of zero.
func (f File) Has(item string) bool {
	return f.items[item]
}

// Load reads and checks the config file name, a YAML mapping of items. It reads nothing and returns the zero File if name
// is "". The error is Problems, with File set to name, for a problem in the file, and any other error if it cannot be read
// or is too large. The messages do not have the values that the file gives.
func Load(name string) (File, error) {
	if name == "" {
		return File{}, nil
	}
	data, err := readFile(name)
	if err != nil {
		return File{}, err
	}
	out, positions, err := yaml.ToJSON(data)
	if err != nil {
		var e *yaml.Error
		if !errors.As(err, &e) {
			return File{}, err
		}
		return File{}, Problems{{File: name, Line: e.Line, Column: e.Column, Msg: e.Msg}}
	}
	r := reader{name: name, positions: positions}
	r.file.items = make(map[string]bool)
	if err := r.read(out); err != nil {
		return File{}, err
	}
	if len(r.problems) > 0 {
		slices.SortStableFunc(r.problems, func(a, b *Problem) int {
			return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
		})
		return File{}, r.problems
	}
	return r.file, nil
}

// readFile returns the content of the file name, which must have at most MaxSize bytes.
func readFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, MaxSize)
	}
	return data, nil
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// reader reads the items of the config file name into file, and collects the problems. positions has the position of
// every value in the file, by JSON Pointer.
type reader struct {
	name      string
	positions map[string]yaml.Position
	file      File
	problems  Problems
}

func (r *reader) fail(ptr, msg string) {
	pos := r.positions[ptr]
	r.problems = append(r.problems, &Problem{File: r.name, Line: pos.Line, Column: pos.Column, Path: ptr, Msg: msg})
}

// read reads data, the JSON that yaml.ToJSON has made of the file. It returns an error only if data is not JSON.
func (r *reader) read(data []byte) error {
	if data[0] != '{' {
		r.fail("", "must be a mapping of items")
		return nil
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	f := &r.file
	for _, key := range slices.Sorted(maps.Keys(items)) {
		ptr, raw := "/"+pointerEscaper.Replace(key), items[key]
		switch key {
		case "data":
			f.Data = r.path(ptr, raw)
		case "site":
			f.Site = r.path(ptr, raw)
		case "theme":
			f.Theme = r.path(ptr, raw)
		case "out":
			f.Out = r.path(ptr, raw)
		case "cache":
			f.Cache = r.path(ptr, raw)
		case "tmp":
			f.Tmp = r.path(ptr, raw)
		case "db":
			f.DB = r.path(ptr, raw)
		case "socket":
			f.Socket = r.socketPath(ptr, raw)
		case "addr":
			f.Addr, _ = r.text(ptr, raw)
		case "origin":
			f.Origin, _ = r.text(ptr, raw)
		case "url":
			f.URL, _ = r.text(ptr, raw)
		case "trustedProxies":
			f.TrustedProxies = r.texts(ptr, raw)
		case "workers":
			n, _ := r.integer(ptr, raw, 0, math.MaxInt, "must be an integer of 0 or more")
			f.Workers = int(n)
		case "memory":
			f.Memory, _ = r.integer(ptr, raw, 0, MaxMemory, fmt.Sprintf("must be an integer from 0 to %d", MaxMemory))
		case "writerUser":
			f.WriterUser = r.account(ptr, raw)
		case "writerGroup":
			f.WriterGroup = r.account(ptr, raw)
		case "readerGroup":
			f.ReaderGroup = r.account(ptr, raw)
		case "maxReaders":
			n, _ := r.integer(ptr, raw, 1, math.MaxInt, "must be an integer of 1 or more")
			f.MaxReaders = int(n)
		case "maxStaged":
			f.MaxStaged, _ = r.integer(ptr, raw, 1, math.MaxInt64, "must be an integer of 1 or more")
		case "commitRate":
			n, _ := r.integer(ptr, raw, 0, math.MaxInt, "must be an integer of 0 or more")
			f.CommitRate = int(n)
		case "minFree":
			f.MinFree, _ = r.integer(ptr, raw, 0, math.MaxInt64, "must be an integer of 0 or more")
		case "minFreeInodes":
			f.MinFreeInodes, _ = r.integer(ptr, raw, 0, math.MaxInt64, "must be an integer of 0 or more")
		case "snapshotIdle":
			f.SnapshotIdle = r.duration(ptr, raw)
		case "stall":
			f.Stall = r.duration(ptr, raw)
		default:
			r.fail(ptr, "unknown item")
			continue
		}
		f.items[key] = true
	}
	return nil
}

// text returns the string raw, the JSON value at ptr, or reports that it is not a string or is empty.
func (r *reader) text(ptr string, raw json.RawMessage) (string, bool) {
	var s string
	if raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		r.fail(ptr, "must be a string")
		return "", false
	}
	if s == "" {
		r.fail(ptr, "must not be empty")
		return "", false
	}
	return s, true
}

// path returns the string raw as a path: from the directory of the config file, unless it is absolute.
func (r *reader) path(ptr string, raw json.RawMessage) string {
	s, ok := r.text(ptr, raw)
	if !ok || filepath.IsAbs(s) {
		return s
	}
	return r.fromDir(s)
}

// fromDir returns the path of s, a relative path, from the directory of the config file.
func (r *reader) fromDir(s string) string {
	// Not filepath.Dir: it cleans the path of the file, as filepath.Join does, and drops "link/..".
	dir, _ := filepath.Split(r.name)
	return fspath.Join(dir, s)
}

// socketPath returns the string raw as the path of a socket: as path does, but a value that starts with @ is kept, whatever
// the directory of the config file is, for unixsock.Listen to refuse.
func (r *reader) socketPath(ptr string, raw json.RawMessage) string {
	s, ok := r.text(ptr, raw)
	if !ok || filepath.IsAbs(s) || strings.HasPrefix(s, "@") {
		return s
	}
	p := r.fromDir(s)
	// fromDir may have cut the "./" that tells a file named @... from an abstract socket.
	if strings.HasPrefix(p, "@") {
		p = "." + string(filepath.Separator) + p
	}
	return p
}

// texts returns the list of strings raw, or reports each value that is not a string or is empty, or that raw is not a
// list.
func (r *reader) texts(ptr string, raw json.RawMessage) []string {
	var items []json.RawMessage
	if raw[0] != '[' || json.Unmarshal(raw, &items) != nil {
		r.fail(ptr, "must be a list of strings")
		return nil
	}
	list := make([]string, 0, len(items))
	for i, item := range items {
		if s, ok := r.text(ptr+"/"+strconv.Itoa(i), item); ok {
			list = append(list, s)
		}
	}
	return list
}

// integer returns the integer raw, from lo to hi, or reports it with msg.
func (r *reader) integer(ptr string, raw json.RawMessage, lo, hi int64, msg string) (int64, bool) {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < lo || n > hi {
		r.fail(ptr, msg)
		return 0, false
	}
	return n, true
}

// account returns the name of a user or a group raw gives, which is a string, or an integer of 0 or more as its decimal
// text. Whether the system knows it is not checked here.
func (r *reader) account(ptr string, raw json.RawMessage) string {
	const msg = "must be a name, or a number of 0 or more"
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
		r.fail(ptr, msg)
		return ""
	}
	n, ok := r.integer(ptr, raw, 0, math.MaxInt64, msg)
	if !ok {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

// duration returns the string raw as a duration of 0 or more, as time.ParseDuration reads it, or reports it.
func (r *reader) duration(ptr string, raw json.RawMessage) time.Duration {
	var s string
	if raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		if d, err := time.ParseDuration(s); err == nil && d >= 0 {
			return d
		}
	}
	r.fail(ptr, "must be a duration of 0 or more, such as 30s or 5m")
	return 0
}
