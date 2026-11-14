//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package build

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestCheckLikeBuild checks that Check returns the errors that Build returns for what it finds before it writes a
// release: those of loading, of the redirects and of the paths of the outputs, in the same order, and nil for the sites
// that Build builds, also those that have warnings.
func TestCheckLikeBuild(t *testing.T) {
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		want   []string // those of Build, when the test knows them
	}{
		{
			name: "a document that does not validate",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Slug = "Services" })
			},
			want: []string{revisionFile(servicesID) + ":4:3: /slug: contains upper case letters; normalize it first"},
		},
		{
			name: "slugs that are used twice",
			change: func(t *testing.T, s *memSource) {
				s.addPage(t, 1, "team", aboutID)
				s.addPage(t, 2, "team", aboutID)
			},
			want: []string{
				revisionFile(newID("ent", 2)) + `:4:3: /slug: slug "team" is also used by ` + revisionFile(newID("ent", 1)),
			},
		},
		{
			name: "a loop of redirects",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/a", "/b")
				s.addRedirect(t, 2, "/b/", "/a/#top")
			},
			want: []string{
				rdr(1) + ":5:3: /to: redirect loop: " + strings.Join([]string{newID("rdr", 1), newID("rdr", 2), newID("rdr", 1)}, " -> "),
				rdr(2) + ":5:3: /to: redirect loop: " + strings.Join([]string{newID("rdr", 1), newID("rdr", 2), newID("rdr", 1)}, " -> "),
			},
		},
		{
			name: "a post at the path of a page",
			change: func(t *testing.T, s *memSource) {
				s.schema.Data = []byte(strings.Replace(testSchema, "url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{slug}/", 1))
				s.editRevision(t, postID, func(r *content.Revision) { r.Slug = "about" })
			},
			want: []string{revisionFile(postID) + `:4:3: /slug: output path "/about/" is also that of ` + revisionFile(aboutID)},
		},
		{
			name: "redirects from the paths of outputs",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/about", "/contact/")
				s.addRedirect(t, 2, "/en/feed", "/en/")
				s.addRedirect(t, 3, "/_forms/"+formID+"/", "/contact/")
				s.addRedirect(t, 5, "/category/news/press-release", "/news/")
			},
			want: []string{
				rdr(1) + `:4:3: /from: from "/about" is the output path "/about/" of ` + revisionFile(aboutID),
				rdr(2) + `:4:3: /from: from "/en/feed" is the output path "/en/feed/" of the feed in en`,
				rdr(3) + `:4:3: /from: from "/_forms/` + formID + `/" is the output path "/_forms/` + formID + `" of ` +
					fileOf(content.RefForm, formID),
				rdr(5) + `:4:3: /from: from "/category/news/press-release" is the output path "/category/news/press-release/" of ` +
					fileOf(content.RefTerm, pressReleaseID),
			},
		},
		{
			name: "a redirect to a medium from the path of a page",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/about", logoID)
			},
			want: []string{rdr(1) + `:4:3: /from: from "/about" is the output path "/about/" of ` + revisionFile(aboutID)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := buildSource(t)
			tt.change(t, src)
			checkErr := Check(t.Context(), src, "")
			_, buildErr := Build(t.Context(), buildOptions(src, t.TempDir(), t.TempDir(), 2))
			if checkErr == nil || buildErr == nil {
				t.Fatalf("Check() = %v and Build() = %v, want errors", checkErr, buildErr)
			}
			got, want := errorTexts(t, checkErr), errorTexts(t, buildErr)
			if !slices.Equal(got, want) {
				t.Errorf("Check() errors:\n%s\nBuild() errors:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Check() errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}

	t.Run("sites that Build builds", func(t *testing.T) {
		for name, src := range map[string]Source{"the fixture": buildSource(t), "the sample site": openSample(t)} {
			res, err := Build(t.Context(), buildOptions(src, t.TempDir(), t.TempDir(), 2))
			if err != nil || len(res.Warnings) == 0 {
				t.Fatalf("Build() of %s = %+v, %v, want a release and warnings", name, res, err)
			}
			if err := Check(t.Context(), src, ""); err != nil {
				t.Errorf("Check() of %s = %v, want nil, though Build has the warnings %v", name, err, res.Warnings)
			}
		}
	})
}

// TestCheckLeavesTheRest checks what Check does not find, which Build finds when it writes the release: an original of
// another size, the path of a file of a medium that a redirect has, and a template that cannot be executed.
func TestCheckLeavesTheRest(t *testing.T) {
	brokenTheme := testTheme()
	brokenTheme["templates/entry.html"] = &fstest.MapFile{Data: []byte("{{.Titel}}\n")}
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
	}{
		{
			name: "an original of another size",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefMedia, logoID, func(m *content.Media) { m.Size++ })
			},
		},
		{
			name: "a redirect from the path of a file of a medium",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/media/"+logoID+"/logo.png", "/")
			},
		},
		{
			name: "a template that cannot be executed",
			change: func(t *testing.T, s *memSource) {
				s.theme = brokenTheme
			},
		},
		{
			name: "a redirect to a medium whose original is not there",
			change: func(t *testing.T, s *memSource) {
				logo := s.putOriginal(t, logoID, "logo.png", testPNG(t, 41, 10))
				delete(s.files, strings.TrimPrefix(logo.Hash, "sha256:"))
				s.addRedirect(t, 1, "/wp-content/uploads/logo.png", logoID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := buildSource(t)
			tt.change(t, src)
			if err := Check(t.Context(), src, ""); err != nil {
				t.Errorf("Check() = %v, want nil", err)
			}
			if res, err := Build(t.Context(), buildOptions(src, t.TempDir(), t.TempDir(), 2)); err == nil {
				t.Errorf("Build() = %+v, want an error: the site is one that Check does not find the problem of", res)
			}
		})
	}
}

// TestCheckURLLikeBuild checks that Check and Build with the same URL that replaces that of the site find the same
// problems, and that both refuse a URL that does not pass in the same words.
func TestCheckURLLikeBuild(t *testing.T) {
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	loop := strings.Join([]string{newID("rdr", 1), newID("rdr", 2), newID("rdr", 1)}, " -> ")
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		want   []string // under stagingURL
	}{
		{
			name: "a redirect to itself",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/staging-only", stagingURL+"/staging-only")
			},
			want: []string{rdr(1) + ":5:3: /to: redirects to itself"},
		},
		{
			name: "a loop of redirects",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/loop-a", stagingURL+"/loop-b")
				s.addRedirect(t, 2, "/loop-b", stagingURL+"/loop-a")
			},
			want: []string{rdr(1) + ":5:3: /to: redirect loop: " + loop, rdr(2) + ":5:3: /to: redirect loop: " + loop},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := buildSource(t)
			tt.change(t, src)
			if err := Check(t.Context(), src, ""); err != nil {
				t.Errorf("Check() = %v, want nil", err)
			}
			if _, err := Build(t.Context(), buildOptions(src, t.TempDir(), t.TempDir(), 2)); err != nil {
				t.Errorf("Build() = %v, want a release", err)
			}
			opts := buildOptions(src, t.TempDir(), t.TempDir(), 2)
			opts.URL = stagingURL
			checkErr := Check(t.Context(), src, stagingURL)
			_, buildErr := Build(t.Context(), opts)
			if checkErr == nil || buildErr == nil {
				t.Fatalf("Check() = %v and Build() = %v, want errors", checkErr, buildErr)
			}
			got, want := errorTexts(t, checkErr), errorTexts(t, buildErr)
			if !slices.Equal(got, want) || !slices.Equal(got, tt.want) {
				t.Errorf("Check() errors:\n%s\nBuild() errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"),
					strings.Join(tt.want, "\n"))
			}
		})
	}

	t.Run("a URL that does not pass", func(t *testing.T) {
		src := buildSource(t)
		for _, bad := range badSiteURLs {
			checkErr := Check(t.Context(), src, bad)
			opts := buildOptions(src, t.TempDir(), t.TempDir(), 2)
			opts.URL = bad
			_, buildErr := Build(t.Context(), opts)
			checkBadURL(t, "Check()", checkErr, bad)
			checkBadURL(t, "Build()", buildErr, bad)
			if checkErr != nil && buildErr != nil && checkErr.Error() != buildErr.Error() {
				t.Errorf("Check() = %q and Build() = %q with the same URL that does not pass, want the same words", checkErr, buildErr)
			}
		}
	})
}
