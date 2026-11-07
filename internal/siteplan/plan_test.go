package siteplan

import "testing"

func TestBasePath(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"https://example.com", ""},
		{"https://example.com/blog", "/blog"},
		{"http://localhost:8080/a/b", "/a/b"},
		{"https://example.com/ブログ", "/ブログ"},
		{"https://example.com/%E3%83%96", "/%E3%83%96"},
	} {
		if got := BasePath(tt.in); got != tt.want {
			t.Errorf("BasePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
