package serve

import (
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func TestFileName(t *testing.T) {
	longest := strings.Repeat("a", content.MaxFileNameBytes)
	for _, tt := range []struct {
		rest string
		name string
		dir  bool
		ok   bool
	}{
		{"/", "", true, true},
		{"/a.html", "a.html", false, true},
		{"/css/x.css", "css/x.css", false, true},
		{"/a/", "a", true, true},
		{"/a/b/", "a/b", true, true},
		{"/%E6%96%B0/", "新", true, true},
		{"/%e6%96%b0/index.html", "新/index.html", false, true},
		{"/新/", "新", true, true},
		{"/a+b", "a+b", false, true},
		{"/a%2Bb", "a+b", false, true},
		{"/a%20b", "a b", false, true},
		{"/a..b", "a..b", false, true},
		{"/" + longest + "/" + longest, longest + "/" + longest, false, true},
		{rest: ""},
		{rest: "a"},
		{rest: "*"},
		{rest: "//"},
		{rest: "//x"},
		{rest: "/a//b"},
		{rest: "/a//"},
		{rest: "/./"},
		{rest: "/."},
		{rest: "/.."},
		{rest: "/a/../b"},
		{rest: "/a/./b"},
		{rest: "/%2E%2E/"},
		{rest: "/.htaccess"},
		{rest: "/%2Ehtaccess"},
		{rest: "/%2ehtaccess"},
		{rest: "/a/.git/config"},
		{rest: "/a%2Fb.html"},
		{rest: "/a%2fb/"},
		{rest: "/a%00b"},
		{rest: "/a%01b"},
		{rest: "/a%7F"},
		{rest: "/a%C2%85"},
		{rest: "/a%zz"},
		{rest: "/a%"},
		{rest: "/a%2"},
		{rest: "/" + longest + "a"},
		{rest: "/" + longest + "a/"},
	} {
		name, dir, ok := fileName(tt.rest)
		if name != tt.name || dir != tt.dir || ok != tt.ok {
			t.Errorf("fileName(%q) = %q, %t, %t, want %q, %t, %t", tt.rest, name, dir, ok, tt.name, tt.dir, tt.ok)
		}
	}
}
