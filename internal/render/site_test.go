package render

import (
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// testPresets returns presets from pairs of names and values.
func testPresets(pairs ...string) []content.Preset {
	var p []content.Preset
	for i := 0; i < len(pairs); i += 2 {
		p = append(p, content.Preset{Name: pairs[i], Value: pairs[i+1]})
	}
	return p
}

// TestSiteCSS checks the rule of the presets in SiteCSS, and that classes.css follows it with the mobileWidth of the
// theme.
func TestSiteCSS(t *testing.T) {
	type siteTest struct {
		name  string
		s     *content.ThemeSettings
		root  string // the rule of :root and a blank line, or ""
		width string // in the media query of classes.css
	}
	tests := []siteTest{
		{name: "no theme", width: "781px"},
		{name: "no presets", s: &content.ThemeSettings{}, width: "781px"},
		{
			name: "every kind, in the order of the kinds and then in the written order",
			s: &content.ThemeSettings{Presets: content.Presets{
				Color:      testPresets("primary", "#1e73be", "accent", "#e85d04"),
				FontSize:   testPresets("large", "clamp(1.25rem, 1rem + 1vw, 1.5rem)"),
				Spacing:    testPresets("30", "1rem", "10", "0.25rem"),
				FontFamily: testPresets("body", `"Noto Sans JP", sans-serif`),
				Gradient:   testPresets("sunset", "linear-gradient(135deg, #f093fb 0%, #f5576c 100%)"),
				Shadow:     testPresets("soft", "0 2px 4px rgba(0, 0, 0, 0.1)", "Soft_2", "none"),
			}},
			root: `:root {
  --color-primary: #1e73be;
  --color-accent: #e85d04;
  --font-size-large: clamp(1.25rem, 1rem + 1vw, 1.5rem);
  --spacing-30: 1rem;
  --spacing-10: 0.25rem;
  --font-family-body: "Noto Sans JP", sans-serif;
  --gradient-sunset: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
  --shadow-soft: 0 2px 4px rgba(0, 0, 0, 0.1);
  --shadow-Soft_2: none;
}

`,
			width: "781px",
		},
		{
			name: "invalid names and values are left out",
			s: &content.ThemeSettings{Presets: content.Presets{
				Color: testPresets(
					"a b", "red", "", "red", "a.b", "red", "a;b", "red", "a:b", "red",
					"x", "red; color: blue", "x", "red}", "x", "", "x", " red", "x", "#000 !important", "x", "a\nb",
					"x", "/* c */ red", "x", "url(x.png)", "x", "</style>", "x", `"a`, "x", "(red",
					"primary", "#1e73be",
				),
				Spacing: testPresets("30", "1rem;", "40", "2rem"),
			}},
			root:  ":root {\n  --color-primary: #1e73be;\n  --spacing-40: 2rem;\n}\n\n",
			width: "781px",
		},
		{
			name: "no valid presets",
			s: &content.ThemeSettings{
				Presets: content.Presets{Color: testPresets("a b", "red"), Shadow: testPresets("x", "")},
			},
			width: "781px",
		},
		{name: "mobileWidth", s: &content.ThemeSettings{MobileWidth: "600px"}, width: "600px"},
		{name: "mobileWidth in em", s: &content.ThemeSettings{MobileWidth: "48.75em"}, width: "48.75em"},
		{name: "mobileWidth in rem", s: &content.ThemeSettings{MobileWidth: ".5rem"}, width: ".5rem"},
	}
	for _, w := range []string{
		"0px", "0", "600", "-600px", "+600px", "1e3px", "600PX", "600 px", " 600px", "600px ", "60vw", "50%",
		"600px;", "600px}", "600px) and (min-width: 1px", "calc(600px)",
	} {
		s := &content.ThemeSettings{MobileWidth: w}
		tests = append(tests, siteTest{name: "invalid mobileWidth " + w, s: s, width: "781px"})
	}
	for _, tt := range tests {
		got := SiteCSS(tt.s)
		classes := strings.Replace(classesCSS, "{{.MobileWidth}}", tt.width, 1)
		root, ok := strings.CutSuffix(got, classes)
		if !ok {
			t.Errorf("%s: SiteCSS() does not end with classes.css with the mobile width %s:\n%s",
				tt.name, tt.width, got)
			continue
		}
		if root != tt.root {
			t.Errorf("%s: SiteCSS() starts with\n%s\nwant\n%s", tt.name, root, tt.root)
		}
		if media := "\n@media (max-width: " + tt.width + ") {\n"; !strings.Contains(got, media) {
			t.Errorf("%s: SiteCSS() lacks %q", tt.name, media)
		}
	}

	// The presets of every kind of content.Presets.
	var s content.ThemeSettings
	v := reflect.ValueOf(&s.Presets).Elem()
	for i := range v.NumField() {
		v.Field(i).Set(reflect.ValueOf(testPresets("x", "0")))
	}
	if got := strings.Count(SiteCSS(&s), "-x: 0;\n"); got != v.NumField() {
		t.Errorf("SiteCSS() has the presets of %d kinds, want %d", got, v.NumField())
	}
}

// TestSiteCSSPresetVars checks that the custom properties of SiteCSS are those that the CSS of pages refers to for the
// same presets.
func TestSiteCSSPresetVars(t *testing.T) {
	th := testTheme(t, map[string]string{"theme.yaml": styleTheme})
	_, css, warnings, err := newRenderer(t, th, "ja", "", nil).Document(decodeBlocks(t, `[{
		"type": "paragraph", "id": "b_0paragr1",
		"style": {
			"textColor": {"preset": "primary"}, "backgroundColor": {"preset": "accent"},
			"gradient": {"preset": "sunset"}, "fontSize": {"preset": "large"}, "fontFamily": {"preset": "body"},
			"padding": {"top": {"preset": "30"}}, "margin": {"top": {"preset": "40"}}
		}
	}]`))
	if err != nil || len(warnings) > 0 {
		t.Fatalf("Document() error: %v, warnings: %q", err, warningStrings(warnings))
	}
	names := func(pattern, s string) []string {
		var n []string
		for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(s, -1) {
			n = append(n, m[1])
		}
		slices.Sort(n)
		return slices.Compact(n)
	}
	used := names(`var\((--[^)]*)\)`, css.String())
	defined := names(`(?m)^  (--[^:]*):`, SiteCSS(th.Settings))
	if len(used) != 7 || !slices.Equal(used, defined) {
		t.Errorf("the CSS of the page refers to %q, and SiteCSS() defines %q; want the same 7", used, defined)
	}
}

// TestClassesCSS checks that classes.css has rules for the classes that the renderer gives blocks, except
// is-layout-flow and those of align, which the theme styles, and for no others, and that its grids are those that the
// CSS of pages assumes.
func TestClassesCSS(t *testing.T) {
	if strings.TrimRight(classesCSS, "\n")+"\n" != classesCSS {
		t.Error("classes.css does not end with exactly one newline")
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(SiteCSS(nil), "")

	lists := []map[string]string{
		textAlignClasses, buttonWidthClasses, columnsAlignClasses, columnAlignClasses, layoutClasses,
	}
	lists = slices.AppendSeq(lists, maps.Values(justifyClasses))
	lists = slices.AppendSeq(lists, maps.Values(verticalAlignClasses))
	want := []string{"is-layout-columns", "is-not-stacked-on-mobile", "is-nowrap"}
	for _, classes := range lists {
		want = slices.AppendSeq(want, maps.Values(classes))
	}
	want = slices.DeleteFunc(want, func(c string) bool { return c == "is-layout-flow" })
	slices.Sort(want)
	want = slices.Compact(want)
	var got []string
	for _, r := range regexp.MustCompile(`([^{}]*)\{[^{}]*\}`).FindAllStringSubmatch(css, -1) {
		for _, c := range regexp.MustCompile(`\.([a-z][a-z0-9-]*)`).FindAllStringSubmatch(r[1], -1) {
			got = append(got, c[1])
		}
	}
	slices.Sort(got)
	got = slices.Compact(got)
	if !slices.Equal(got, want) {
		t.Errorf("classes in the selectors of classes.css:\n%q\nwant:\n%q", got, want)
	}

	// gridColumns assumes the default gap of grids, and the default columns are those of a minColumnWidth of 12rem.
	gapPattern := regexp.MustCompile(`\.is-layout-grid\b[^{}]*\{[^{}]*\bgap: ([^;]*);`)
	if gaps := gapPattern.FindAllStringSubmatch(css, -1); len(gaps) != 1 ||
		!strings.Contains(gridColumns(2, "1px", ""), "(100% - 1 * "+gaps[0][1]+") / 2") {
		t.Errorf("the gaps of grids in classes.css: %q, want one, that of %s", gaps, gridColumns(2, "1px", ""))
	}
	columnsPattern := regexp.MustCompile(`(?m)^\.is-layout-grid \{[^{}]*\bgrid-template-columns: ([^;]*);`)
	if columns, want := columnsPattern.FindAllStringSubmatch(css, -1), gridColumns(0, "12rem", ""); len(columns) != 1 ||
		columns[0][1] != want {
		t.Errorf("the columns of grids in classes.css: %q, want %s", columns, want)
	}
}
