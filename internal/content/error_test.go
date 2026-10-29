package content

import (
	"errors"
	"reflect"
	"testing"
)

func errorPaths(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	var es Errors
	if !errors.As(err, &es) {
		t.Fatalf("error %q (%T) is not Errors", err, err)
	}
	paths := make([]string, len(es))
	for i, e := range es {
		paths[i] = e.Path
	}
	return paths
}

func TestError(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{name: "root", err: &Error{Path: "", Msg: "m"}, want: "m"},
		{name: "nested", err: &Error{Path: "/a/0", Msg: "m"}, want: "/a/0: m"},
		{name: "position", err: &Error{Path: "/a/0", Msg: "m", Line: 3, Column: 15}, want: "3:15: /a/0: m"},
		{
			name: "file and position",
			err:  &Error{Path: "/types/works/url", Msg: "m", File: "site/schema.yaml", Line: 12, Column: 5},
			want: "site/schema.yaml:12:5: /types/works/url: m",
		},
		{name: "file", err: &Error{Path: "/a", Msg: "m", File: "settings.json"}, want: "settings.json: /a: m"},
		{name: "file at the root", err: &Error{Msg: "m", File: "settings.json"}, want: "settings.json: m"},
		{name: "position at the root", err: &Error{Msg: "m", Line: 1, Column: 1}, want: "1:1: m"},
		{
			name: "file and position without a path",
			err:  &Error{Msg: "unexpected end of JSON input", File: "settings.json", Line: 4, Column: 2},
			want: "settings.json:4:2: unexpected end of JSON input",
		},
		{name: "column without a line", err: &Error{Path: "/a", Msg: "m", Column: 7}, want: "/a: m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name string
		errs Errors
		want string
	}{
		{name: "one", errs: Errors{{Path: "/a", Msg: "m"}}, want: "/a: m"},
		{name: "two", errs: Errors{{Path: "", Msg: "m"}, {Path: "/a/0", Msg: "n"}}, want: "m\n/a/0: n"},
		{
			name: "files",
			errs: Errors{
				{Msg: "m", File: "settings.json", Line: 1, Column: 1},
				{Path: "/types", Msg: "n", File: "site/schema.yaml", Line: 2, Column: 1},
			},
			want: "settings.json:1:1: m\nsite/schema.yaml:2:1: /types: n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.errs.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChecker(t *testing.T) {
	tests := []struct {
		name  string
		run   func(c *checker)
		paths []string
		msg   string
	}{
		{name: "empty", run: func(*checker) {}},
		{name: "nil error", run: func(c *checker) { c.add("/a", nil) }},
		{
			name: "in order",
			run: func(c *checker) {
				c.addf("/b", "unknown mark type %q", "blink")
				c.add("", errors.New("empty object"))
				c.add("/a", nil)
				c.addf("/a/0", "empty text")
			},
			paths: []string{"/b", "", "/a/0"},
			msg:   "/b: unknown mark type \"blink\"\nempty object\n/a/0: empty text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c checker
			tt.run(&c)
			err := c.result()
			if tt.paths == nil && err != nil {
				t.Fatalf("result() = %#v, want nil", err)
			}
			if got := errorPaths(t, err); !reflect.DeepEqual(got, tt.paths) {
				t.Errorf("paths = %q, want %q", got, tt.paths)
			}
			if err != nil && err.Error() != tt.msg {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.msg)
			}
		})
	}
}
