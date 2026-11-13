package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

// maxConfigSize is the most bytes of a config file.
const maxConfigSize = 1 << 20

// config holds the items of a config file; those that the file does not give are zero. The paths that the file gives
// are relative to its directory, so that they are the paths to open from the working directory, or absolute.
type config struct {
	data, site, theme, out, cache string
	addr, socket, origin          string
	trustedProxies                []string
	workers                       int
	memory                        int64 // MiB
}

// loadConfig reads and checks the config file file, a YAML mapping of items. It reads nothing and returns the zero
// config if file is "". The error is content.Errors, with File set to file, for a problem in the file, and any other
// error if it cannot be read or is too large. The messages do not have the values that the file gives.
func loadConfig(file string) (config, error) {
	if file == "" {
		return config{}, nil
	}
	data, err := readConfig(file)
	if err != nil {
		return config{}, err
	}
	out, positions, err := yaml.ToJSON(data)
	if err != nil {
		var e *yaml.Error
		if !errors.As(err, &e) {
			return config{}, err
		}
		return config{}, content.Errors{{File: file, Line: e.Line, Column: e.Column, Msg: e.Msg}}
	}
	r := configReader{file: file, positions: positions}
	if err := r.read(out); err != nil {
		return config{}, err
	}
	if len(r.errs) > 0 {
		slices.SortStableFunc(r.errs, func(a, b *content.Error) int {
			return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
		})
		return config{}, r.errs
	}
	return r.cfg, nil
}

// readConfig returns the content of the file, which must have at most maxConfigSize bytes.
func readConfig(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", file, maxConfigSize)
	}
	return data, nil
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// configReader reads the items of the config file in file into cfg, and collects the problems in errs. positions has
// the position of every value in the file, by JSON Pointer.
type configReader struct {
	file      string
	positions map[string]yaml.Position
	cfg       config
	errs      content.Errors
}

func (r *configReader) fail(ptr, msg string) {
	pos := r.positions[ptr]
	r.errs = append(r.errs, &content.Error{File: r.file, Line: pos.Line, Column: pos.Column, Path: ptr, Msg: msg})
}

// read reads data, the JSON that yaml.ToJSON has made of the file. It returns an error only if data is not JSON.
func (r *configReader) read(data []byte) error {
	if data[0] != '{' {
		r.fail("", "must be a mapping of items")
		return nil
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(items)) {
		ptr, raw := "/"+pointerEscaper.Replace(key), items[key]
		switch key {
		case "data":
			r.cfg.data = r.path(ptr, raw)
		case "site":
			r.cfg.site = r.path(ptr, raw)
		case "theme":
			r.cfg.theme = r.path(ptr, raw)
		case "out":
			r.cfg.out = r.path(ptr, raw)
		case "cache":
			r.cfg.cache = r.path(ptr, raw)
		case "socket":
			r.cfg.socket = r.path(ptr, raw)
		case "addr":
			r.cfg.addr, _ = r.text(ptr, raw)
		case "origin":
			r.cfg.origin, _ = r.text(ptr, raw)
		case "trustedProxies":
			r.cfg.trustedProxies = r.texts(ptr, raw)
		case "workers":
			n, _ := r.integer(ptr, raw, math.MaxInt, "must be an integer of 0 or more")
			r.cfg.workers = int(n)
		case "memory":
			r.cfg.memory, _ = r.integer(ptr, raw, maxMemory, fmt.Sprintf("must be an integer from 0 to %d", maxMemory))
		default:
			r.fail(ptr, "unknown item")
		}
	}
	return nil
}

// text returns the string raw, the JSON value at ptr, or reports that it is not a string or is empty.
func (r *configReader) text(ptr string, raw json.RawMessage) (string, bool) {
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
func (r *configReader) path(ptr string, raw json.RawMessage) string {
	s, ok := r.text(ptr, raw)
	if !ok || filepath.IsAbs(s) {
		return s
	}
	return filepath.Join(filepath.Dir(r.file), s)
}

// texts returns the list of strings raw, or reports each value that is not a string or is empty, or that raw is not a
// list.
func (r *configReader) texts(ptr string, raw json.RawMessage) []string {
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

// integer returns the integer raw, from 0 to max, or reports it with msg.
func (r *configReader) integer(ptr string, raw json.RawMessage, max int64, msg string) (int64, bool) {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < 0 || n > max {
		r.fail(ptr, msg)
		return 0, false
	}
	return n, true
}

// flagGiven reports whether the command line gave the flag name, also with its default value.
func flagGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}

// printConfigError prints on w the error err of loadConfig for command, such as "whiteroom build": a line for each
// problem in the file, and any other error after the name of the command.
func printConfigError(w io.Writer, command string, err error) {
	var es content.Errors
	if !errors.As(err, &es) {
		printf(w, "%s: %v\n", command, err)
		return
	}
	for _, e := range es {
		printf(w, "%s\n", e.Error())
	}
}
