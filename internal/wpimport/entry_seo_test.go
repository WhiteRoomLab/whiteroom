package wpimport

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func TestSEOPluginTable(t *testing.T) {
	want := []struct {
		name, label string
		keys        []string
	}{
		{"yoast", "Yoast SEO", []string{
			"_yoast_wpseo_title", "_yoast_wpseo_metadesc", "_yoast_wpseo_canonical", "_yoast_wpseo_meta-robots-noindex",
			"_yoast_wpseo_opengraph-image-id", "_yoast_wpseo_opengraph-image", "_yoast_wpseo_primary_category",
			"_yoast_wpseo_opengraph-title", "_yoast_wpseo_opengraph-description", "_yoast_wpseo_twitter-title", "_yoast_wpseo_twitter-description",
			"_yoast_wpseo_twitter-image", "_yoast_wpseo_meta-robots-nofollow", "_yoast_wpseo_meta-robots-adv", "_yoast_wpseo_bctitle",
			"_yoast_wpseo_redirect",
		}},
		{"rankmath", "Rank Math", []string{
			"rank_math_title", "rank_math_description", "rank_math_canonical_url", "rank_math_robots", "rank_math_facebook_image_id",
			"rank_math_facebook_image", "rank_math_facebook_title", "rank_math_facebook_description", "rank_math_twitter_title",
			"rank_math_twitter_description", "rank_math_twitter_image", "rank_math_breadcrumb_title",
		}},
		{"seopress", "SEOPress", []string{
			"_seopress_titles_title", "_seopress_titles_desc", "_seopress_robots_canonical", "_seopress_robots_index",
			"_seopress_social_fb_img_attachment_id", "_seopress_social_fb_img", "_seopress_social_fb_title", "_seopress_social_fb_desc",
			"_seopress_social_twitter_title", "_seopress_social_twitter_desc", "_seopress_social_twitter_img",
			"_seopress_robots_follow", "_seopress_robots_snippet", "_seopress_robots_imageindex",
		}},
		{"tsf", "The SEO Framework", []string{
			"_genesis_title", "_genesis_description", "_genesis_canonical_uri", "_genesis_noindex", "_social_image_id",
			"_social_image_url", "_open_graph_title", "_open_graph_description", "_twitter_title", "_twitter_description",
			"_genesis_nofollow", "_genesis_noarchive", "redirect", "exclude_local_search", "exclude_from_archive", "_tsf_title_no_blogname",
		}},
		{"ssp", "SEO SIMPLE PACK", []string{
			"ssp_meta_title", "ssp_meta_description", "ssp_meta_canonical", "ssp_meta_robots", "ssp_meta_image",
		}},
		{"aioseo", "All in One SEO", []string{
			"_aioseo_title", "_aioseo_description", "_aioseo_og_title", "_aioseo_og_description", "_aioseo_twitter_title",
			"_aioseo_twitter_description",
		}},
	}
	if len(seoPlugins) != len(want) {
		t.Fatalf("%d plugins, want %d", len(seoPlugins), len(want))
	}
	seen := make(map[string]string)
	for i, w := range want {
		p := &seoPlugins[i]
		if p.name != w.name || p.label != w.label {
			t.Errorf("plugin %d is %q (%q), want %q (%q)", i, p.name, p.label, w.name, w.label)
		}
		if got := p.keys(); !slices.Equal(got, w.keys) {
			t.Errorf("the keys of %s are\n%q\nwant\n%q", w.name, got, w.keys)
		}
		for _, k := range p.keys() {
			if other, ok := seen[k]; ok {
				t.Errorf("the key %q is that of %s and of %s", k, other, p.name)
			}
			seen[k] = p.name
		}
		if seoPluginNamed(w.name) != p {
			t.Errorf("seoPluginNamed(%q) is not the plugin", w.name)
		}
	}
	for _, name := range []string{"", "Yoast", "yoast ", "none"} {
		if p := seoPluginNamed(name); p != nil {
			t.Errorf("seoPluginNamed(%q) = %q, want none", name, p.name)
		}
	}
}

func TestSEOVariableKinds(t *testing.T) {
	for _, tt := range []struct {
		plugin                          string
		title, site, sep, page, excerpt []string
		other                           []string
	}{
		{"yoast", []string{"title"}, []string{"sitename"}, []string{"sep"}, []string{"page", "pagenumber", "pagetotal"},
			[]string{"excerpt", "excerpt_only"}, []string{"Title", "post_title", "sitetitle", "category", "separator_sa", ""}},
		{"rankmath", []string{"title"}, []string{"sitename"}, []string{"sep"}, []string{"page", "pagenumber", "pagetotal"},
			[]string{"excerpt", "excerpt_only", "seo_description"}, []string{"Title", "post_title", "site_title", "category"}},
		{"seopress", []string{"post_title", "title"}, []string{"sitetitle", "sitename"}, []string{"sep"}, []string{"current_pagination", "page"},
			[]string{"post_excerpt", "excerpt"}, []string{"Title", "pagenumber", "site_title", "category"}},
		{"tsf", nil, nil, nil, nil, nil, []string{"title", "sitename", "sep", "page", "excerpt"}},
		{"ssp", []string{"page_title"}, []string{"site_title"}, []string{"sep"}, []string{"page"},
			[]string{"page_contents"}, []string{"title", "post_title", "sitename", "term_name", "Page_Title"}},
		// The names are cases, and the others are those of Yoast SEO.
		{"aioseo", []string{"post_title", "Post_Title", "POST_TITLE"}, []string{"site_title", "SITE_TITLE"}, []string{"separator_sa", "Separator_SA"},
			[]string{"page_number", "PAGE_NUMBER"}, []string{"post_excerpt", "post_excerpt_only", "post_content", "POST_EXCERPT"},
			[]string{"title", "sitename", "sep", "page", "excerpt", "author_name"}},
	} {
		v := &seoPluginNamed(tt.plugin).vars
		for kind, names := range map[varKind][]string{
			varTitle: tt.title, varSite: tt.site, varSep: tt.sep, varPage: tt.page, varExcerpt: tt.excerpt, varOther: tt.other,
		} {
			for _, name := range names {
				if got := v.kind(name); got != kind {
					t.Errorf("%s: the kind of %q is %d, want %d", tt.plugin, name, got, kind)
				}
			}
		}
	}
}

// seoRow is a title or a description that a plugin wrote, with what is made of it.
type seoRow struct {
	name, value string
	want        seoText
}

func seoWant(text string) seoText { return seoText{text: text} }

func seoUnknown(vars ...string) seoText { return seoText{unknown: vars} }

func runSEOText(t *testing.T, plugin string, rows []seoRow, title, site string, convert func(p *seoPlugin, value, title, site string) seoText) {
	t.Helper()
	p := seoPluginNamed(plugin)
	if p == nil {
		t.Fatalf("no plugin %q", plugin)
	}
	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			got := convert(p, tt.value, title, site)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s of %q = %+v, want %+v", plugin, tt.value, got, tt.want)
			}
			if got.text != "" {
				if err := content.CheckLine(got.text); err != nil {
					t.Errorf("the text %q of %q is not a line: %v", got.text, tt.value, err)
				}
			}
		})
	}
}

func titleOf(p *seoPlugin, value, title, site string) seoText { return p.titleText(value, title, site) }

func descriptionOf(p *seoPlugin, value, title, site string) seoText {
	return p.descriptionText(value, title, site)
}

func TestSEOTitleYoast(t *testing.T) {
	combining := "e" + string(rune(0x301))
	runSEOText(t, "yoast", []seoRow{
		{"the format of the title and nothing else", "%%title%% %%page%% %%sep%% %%sitename%%", seoText{}},
		{"the title alone", "%%title%%", seoText{}},
		{"the separator alone", "%%sep%%", seoText{}},
		{"the name of the site alone", "%%sitename%%", seoText{}},
		{"the number of the page alone", "%%page%%", seoText{}},
		{"the name of the site first", "%%sitename%% %%sep%% %%title%%", seoText{}},
		{"the numbers of the pages", "%%title%% %%pagenumber%% %%pagetotal%%", seoText{}},
		{"the page again", "%%page%% %%title%% %%page%%", seoText{}},
		{"text", "Custom title", seoWant("Custom title")},
		{"text with the title and the format", "Custom %%title%% %%sep%% %%sitename%%", seoWant("Custom About")},
		{"the title in text", "Welcome to %%title%%", seoWant("Welcome to About")},
		{"the title twice", "%%title%% %%title%%", seoWant("About About")},
		{"text and the separator and the name of the site", "Custom %%sep%% %%sitename%%", seoWant("Custom")},
		{"a name of the site that is written", "Custom | Test", seoWant("Custom")},
		{"a name of the site first", "Test | Custom", seoWant("Custom")},
		{"a name of the site in both", "Test - Custom - Test", seoWant("Custom")},
		{"a name of the site and no more text", "Test", seoText{}},
		{"white space and the name of the site", "  Test  ", seoText{}},
		{"a separator that is written next to a variable", "Custom | %%sitename%%", seoWant("Custom")},
		{"separators that are written next to variables", "Custom -|- %%sep%% %%sitename%%", seoWant("Custom")},
		{"a separator that is written before the title stays: no variable was taken off next to it", "- %%title%% %%sep%%", seoWant("- About")},
		{"a separator that is written after the name of the site that is first", "%%sitename%% | Custom", seoWant("Custom")},
		{"a separator that is written after the separator that is first", "%%sep%% Custom - %%page%%", seoWant("Custom")},
		{"a variable with a quote in its name", `%%a"b%% x`, seoUnknown(`%%a"b%%`)},
		{"a variable with a backslash in its name", `%%a\b%% x`, seoUnknown(`%%a\b%%`)},
		{"a variable that is long", "%%" + strings.Repeat("x", 50) + "%% y", seoUnknown("%%" + strings.Repeat("x", 50) + "%%")},
		{"all the separators", "Custom - – — | ｜ · • : ： / ／ ~ « » < > * %%sitename%%", seoWant("Custom")},
		{"a name of the site that no separator sets off", "Tokyo Test", seoWant("Tokyo Test")},
		{"a name that ends a word", "AboutTest", seoWant("AboutTest")},
		{"a name of the site that starts the text", "Test Tower access", seoWant("Test Tower access")},
		{"a name of the site that a separator sets off", "Test | Tower access", seoWant("Tower access")},
		{"a name of the site and a middle dot that is not a separator", "About us・Test", seoWant("About us・Test")},
		{"a name of the site in the middle", "About Test us", seoWant("About Test us")},
		{"a name of the site in the middle between separators", "A | Test | B", seoWant("A | Test | B")},
		{"a name that is not the whole of the end", "About | Test2", seoWant("About | Test2")},
		{"a lower case name of the site", "About | test", seoWant("About | test")},
		{"a percent sign", "100% natural", seoWant("100% natural")},
		{"percent signs", "100%% natural", seoWant("100%% natural")},
		{"line breaks", "Line one\nline two\r\nline three\ttab", seoWant("Line one line two  line three tab")},
		{"control characters", "A" + string(rune(1)) + "B" + string(rune(0x7f)), seoWant("AB")},
		{"a combining character", combining, seoWant(string(rune(0xe9)))},
		{"a variable that is not replaced", "%%category%% news", seoUnknown("%%category%%")},
		{"variables that are not replaced, once each, in order", "%%title%% %%category%% %%tag%% %%category%% %%cf_price%%", seoUnknown("%%category%%", "%%tag%%", "%%cf_price%%")},
		{"the separator in the middle", "%%title%% in %%sep%% the middle", seoUnknown("%%sep%%")},
		{"the name of the site in the middle", "Name: %%sitename%% in the middle", seoUnknown("%%sitename%%")},
		{"the excerpt", "%%excerpt%%", seoUnknown("%%excerpt%%")},
		{"a name with a space", "%%cf_my field%%", seoUnknown("%%cf_my field%%")},
		{"names are cases", "%%Title%% %%SEP%%", seoUnknown("%%Title%%", "%%SEP%%")},
		{"a variable that the replacement makes", "%%%%title%%%%", seoUnknown("%%About%%")},
		{"percent signs around a variable", "%%%%sep%%%% x", seoUnknown("%%sep%%")},
		{"a variable with a space as its name", "a %% %% b", seoUnknown("%% %%")},
		{"percent signs only", "%%%%", seoWant("%%%%")},
		{"an opener that is not closed", "%%title", seoWant("%%title")},
		{"a variable that is not closed with two", "%%title%", seoWant("%%title%")},
		{"a variable with one percent sign each side", "%title%", seoWant("%title%")},
		{"a variable and then an opener", "%%title%% %%", seoWant("About %%")},
	}, "About", "Test", titleOf)
}

func TestSEOTitleRankMath(t *testing.T) {
	runSEOText(t, "rankmath", []seoRow{
		{"the format of the title and nothing else", "%title% %page% %sep% %sitename%", seoText{}},
		{"the numbers of the pages", "%title% %pagenumber% %pagetotal%", seoText{}},
		{"text with the format", "Custom %title% %sep% %sitename%", seoWant("Custom About")},
		{"a name of the site that is written", "Custom | Test", seoWant("Custom")},
		{"a variable with an argument", "%customfield(price)% yen", seoUnknown("%customfield(price)%")},
		{"a name with dashes and an argument that has spaces", "%my-var(a b)% x", seoUnknown("%my-var(a b)%")},
		{"a variable with an argument that has spaces", "%date(F j, Y)% news", seoUnknown("%date(F j, Y)%")},
		{"an argument on a variable that has none", "%title(x)% news", seoUnknown("%title(x)%")},
		{"an argument that is not closed", "%count(x news", seoWant("%count(x news")},
		{"an argument with no name", "%(a b)% x", seoWant("%(a b)% x")},
		{"a name, a space and a closing bracket", "%title x)% y", seoWant("%title x)% y")},
		{"an argument that is closed but not by a sign", "%a(b) x%", seoWant("%a(b) x%")},
		{"an argument that ends at a sign that is followed by a sign", "%a(b%% x", seoUnknown("%a(b%")},
		{"a variable that is not closed", "%title news", seoWant("%title news")},
		{"percent signs in text", "100% natural and 50% off", seoWant("100% natural and 50% off")},
		{"a range", "Sale 50%-100% off", seoUnknown("%-100%")},
		{"the excerpt in a title", "%excerpt%", seoUnknown("%excerpt%")},
		{"one sign", "%", seoWant("%")},
		{"two signs", "%%", seoWant("%%")},
		{"names are cases", "%Title%", seoUnknown("%Title%")},
		{"the title of Yoast SEO, which is a variable and a sign on each side", "%%title%%", seoUnknown("%About%")},
		{"an argument that is not closed before the next sign", "%a(%a(%a(", seoUnknown("%a(%")},
		{"a variable of a name and an argument, and what follows it", "%a(b)%c(d)%", seoUnknown("%a(b)%")},
	}, "About", "Test", titleOf)
}

func TestSEOTitleSEOPress(t *testing.T) {
	runSEOText(t, "seopress", []seoRow{
		{"the format of the title and nothing else", "%%post_title%% %%sep%% %%sitetitle%%", seoText{}},
		{"the other names", "%%title%% %%current_pagination%% %%page%% %%sitename%%", seoText{}},
		{"text with the format", "Custom %%post_title%% %%sep%% %%sitetitle%%", seoWant("Custom About")},
		{"a name of the site that is written", "Custom | Test", seoWant("Custom")},
		{"a variable that is not replaced", "%%post_category%% news", seoUnknown("%%post_category%%")},
		{"names of Yoast that SEOPress does not have", "%%pagenumber%% %%sitetitle%%", seoUnknown("%%pagenumber%%")},
		{"the excerpt", "%%post_excerpt%%", seoUnknown("%%post_excerpt%%")},
	}, "About", "Test", titleOf)
}

func TestSEOTitleTSF(t *testing.T) {
	// The SEO Framework has no variables: what looks like one is text.
	runSEOText(t, "tsf", []seoRow{
		{"text", "Custom title", seoWant("Custom title")},
		{"the same as the title", "About", seoText{}},
		{"a name of the site that is written", "Custom | Test", seoWant("Custom")},
		{"the name of the site first", "Test - Custom", seoWant("Custom")},
		{"what looks like variables", "%%title%% %%sep%% %%sitename%%", seoWant("%%title%% %%sep%% %%sitename%%")},
		{"what looks like variables of other plugins", "%title% %_page_title_% #post_title", seoWant("%title% %_page_title_% #post_title")},
		{"a name that is the title", "Test", seoText{}},
	}, "About", "Test", titleOf)
}

func TestSEOTitleSSP(t *testing.T) {
	runSEOText(t, "ssp", []seoRow{
		{"the format of the title and nothing else", "%_page_title_% %_sep_% %_site_title_%", seoText{}},
		{"the number of the page", "%_page_title_% %_page_%", seoText{}},
		{"text with the format", "Custom %_page_title_% %_sep_% %_site_title_%", seoWant("Custom About")},
		{"the name of the site first", "%_site_title_% %_sep_% Custom", seoWant("Custom")},
		{"a name of the site that is written", "Custom | Test", seoWant("Custom")},
		{"a variable that is not replaced", "%_term_name_% archive", seoUnknown("%_term_name_%")},
		{"a variable with underscores in its name", "%_a_b_c_%", seoUnknown("%_a_b_c_%")},
		{"the tagline", "%_site_title_% %_tagline_%", seoUnknown("%_tagline_%")},
		{"names of other plugins", "%%title%% %title%", seoWant("%%title%% %title%")},
		{"no name", "%__% x", seoWant("%__% x")},
		{"a name that does not end with an underscore", "%_page_title% x", seoWant("%_page_title% x")},
		{"a name without the underscore after the sign", "%page_title_% x", seoWant("%page_title_% x")},
		{"a name that is not closed", "%_page_title_ x", seoWant("%_page_title_ x")},
		{"an underscore after the percent sign", "%_ x", seoWant("%_ x")},
		{"the percent sign of a number", "100%_ natural", seoWant("100%_ natural")},
	}, "About", "Test", titleOf)
}

func TestSEOTitleAIOSEO(t *testing.T) {
	runSEOText(t, "aioseo", []seoRow{
		{"the format of the title and nothing else", "#post_title #separator_sa #site_title", seoText{}},
		{"the number of the page", "#post_title #page_number", seoText{}},
		{"text with the format", "Custom #post_title #separator_sa #site_title", seoWant("Custom About")},
		{"names are not cases", "#Post_Title #SEPARATOR_SA #Site_Title", seoText{}},
		{"a name of the site that is written", "Custom | Test", seoWant("Custom")},
		{"a number", "Episode #1", seoWant("Episode #1")},
		{"a hash at the end", "C#", seoWant("C#")},
		{"a hash alone", "# title", seoWant("# title")},
		{"what follows a name that is not a variable", "Tokyo #food guide", seoUnknown("#food")},
		{"a name that goes on", "#post_titles", seoUnknown("#post_titles")},
		{"a name that goes on after the name of a variable", "#post_title_alt", seoUnknown("#post_title_alt")},
		{"a custom field", "#custom_field-price yen", seoUnknown("#custom_field-price")},
		{"a custom field in the case of its name", "#Custom_Field-price-2 yen", seoUnknown("#Custom_Field-price-2")},
		{"a custom field with no name", "#custom_field yen", seoUnknown("#custom_field")},
		{"a variable that ends with a dash, next to one that is taken off", "#custom_field-a-#separator_sa", seoUnknown("#custom_field-a-")},
		{"a custom field with a name that is only a dash", "#custom_field- yen", seoUnknown("#custom_field")},
		{"a taxonomy", "#tax_name-category news", seoUnknown("#tax_name-category")},
		{"the title and a dash that is not part of the variable, and the name of the site", "#post_title-Test", seoText{}},
		{"the title and a dash that is not part of the variable", "#post_title-guide", seoWant("About-guide")},
		{"the title and text", "#post_title: guide", seoWant("About: guide")},
		{"the excerpt", "#post_excerpt", seoUnknown("#post_excerpt")},
		{"percent signs", "%%title%% 100%", seoWant("%%title%% 100%")},
	}, "About", "Test", titleOf)
}

func TestSEOTitleThatEndsWithASeparator(t *testing.T) {
	// The title is not trimmed where what is next to it is taken off.
	runSEOText(t, "yoast", []seoRow{
		{"the separator next to the title", "%%title%%%%sep%%", seoText{}},
		{"the name of the site next to the title", "%%sitename%%%%title%%", seoText{}},
		{"the same, and more text", "Custom %%title%%%%sep%%", seoWant("Custom About -")},
	}, "About -", "Test", titleOf)
}

func TestSEOTitleNothingToReplaceWith(t *testing.T) {
	// A draft that has no title.
	runSEOText(t, "yoast", []seoRow{
		{"the title alone", "%%title%%", seoText{}},
		{"the format", "%%title%% %%sep%% %%sitename%%", seoText{}},
		{"text and the title", "Custom %%title%%", seoWant("Custom")},
		{"text", "Custom", seoWant("Custom")},
		{"the title between", "A %%title%% B", seoWant("A  B")},
	}, "", "Test", titleOf)
	// A site that has no name: nothing is written by hand.
	runSEOText(t, "yoast", []seoRow{
		{"a name that is not there", "Custom | Test", seoWant("Custom | Test")},
		{"the variable of it", "Custom %%sep%% %%sitename%%", seoWant("Custom")},
		{"a separator at the end, which is not next to anything that is taken off", "Custom |", seoWant("Custom |")},
		{"a separator at the start", "| Custom", seoWant("| Custom")},
	}, "About", "", titleOf)
	// A title that holds what is taken off: it is the title, so it stays.
	runSEOText(t, "yoast", []seoRow{
		{"the title with the name of the site in it", "Custom %%title%%", seoWant("Custom About | Test")},
		{"the title as it is", "%%title%%", seoText{}},
		{"the title with a separator after it, which stays", "%%title%% -", seoWant("About | Test -")},
	}, "About | Test", "Test", titleOf)
}

func TestSEODescription(t *testing.T) {
	runSEOText(t, "yoast", []seoRow{
		{"text", "A description", seoWant("A description")},
		{"the title and the name of the site", "%%title%% on %%sitename%%", seoWant("About on Test")},
		{"the title is not taken off", "%%title%%", seoWant("About")},
		{"the name of the site is not taken off", "Test | A description | Test", seoWant("Test | A description | Test")},
		{"the excerpt", "%%excerpt%%", seoText{}},
		{"the excerpt of the other kind", "%%excerpt_only%%", seoText{}},
		{"the excerpts and white space", " %%excerpt%% \n %%excerpt_only%% ", seoText{}},
		{"the excerpt and text", "%%excerpt%% Read more", seoUnknown("%%excerpt%%")},
		{"the excerpt and a variable that is replaced", "%%excerpt%% %%title%%", seoUnknown("%%excerpt%%")},
		{"the excerpt and another variable", "%%excerpt%% %%category%%", seoUnknown("%%excerpt%%", "%%category%%")},
		{"a variable that is not replaced", "%%category%% news", seoUnknown("%%category%%")},
		{"the separator", "A %%sep%% B", seoUnknown("%%sep%%")},
		{"the number of the page", "A %%page%%", seoUnknown("%%page%%")},
		{"line breaks", "One\ntwo\r\nthree", seoWant("One two  three")},
		{"a variable that the replacement makes", "%%%%title%%%%", seoUnknown("%%About%%")},
		{"percent signs", "100% natural", seoWant("100% natural")},
	}, "About", "Test", descriptionOf)
	runSEOText(t, "yoast", []seoRow{
		{"the name of the site that is empty", "A %%sitename%%", seoWant("A")},
		{"only the name of the site", "%%sitename%%", seoText{}},
		{"only the title", "%%title%%", seoText{}},
	}, "", "", descriptionOf)
	runSEOText(t, "rankmath", []seoRow{
		{"the title", "%title% - %sitename%", seoWant("About - Test")},
		{"the excerpt of Rank Math", "%seo_description%", seoText{}},
		{"the excerpt only", "%excerpt_only%", seoText{}},
		{"the excerpt", "%excerpt%", seoText{}},
		{"an argument", "%customfield(x)%", seoUnknown("%customfield(x)%")},
	}, "About", "Test", descriptionOf)
	runSEOText(t, "seopress", []seoRow{
		{"the title", "%%post_title%% - %%sitetitle%%", seoWant("About - Test")},
		{"the excerpt", "%%post_excerpt%%", seoText{}},
		{"the other excerpt", "%%excerpt%%", seoText{}},
	}, "About", "Test", descriptionOf)
	runSEOText(t, "tsf", []seoRow{
		{"text", "A description", seoWant("A description")},
		{"what looks like a variable", "%%excerpt%% #post_excerpt", seoWant("%%excerpt%% #post_excerpt")},
	}, "About", "Test", descriptionOf)
	runSEOText(t, "ssp", []seoRow{
		{"the title", "%_page_title_% - %_site_title_%", seoWant("About - Test")},
		{"the contents of the page", "%_page_contents_%", seoText{}},
		{"the term", "%_term_description_%", seoUnknown("%_term_description_%")},
	}, "About", "Test", descriptionOf)
	runSEOText(t, "aioseo", []seoRow{
		{"the title", "#post_title - #site_title", seoWant("About - Test")},
		{"the excerpt", "#post_excerpt", seoText{}},
		{"the excerpt only", "#post_excerpt_only", seoText{}},
		{"the content", "#post_content", seoText{}},
		{"the content in text", "#post_content here", seoUnknown("#post_content")},
		{"the author", "#author_first_name", seoUnknown("#author_first_name")},
		{"a number", "Episode #1", seoWant("Episode #1")},
	}, "About", "Test", descriptionOf)
}

func TestSEOTooLong(t *testing.T) {
	p := seoPluginNamed("yoast")
	title := strings.Repeat("a", 100)
	// The title is put in 20,000 times.
	got := p.titleText("x "+strings.Repeat("%%title%%", 20000), title, "Test")
	if want := (seoText{long: 2 + 20000*100}); !reflect.DeepEqual(got, want) {
		t.Errorf("titleText = %+v, want %+v", got, want)
	}
	got = p.descriptionText(strings.Repeat("%%title%%", 20000), title, "Test")
	if want := (seoText{long: 20000 * 100}); !reflect.DeepEqual(got, want) {
		t.Errorf("descriptionText = %+v, want %+v", got, want)
	}
	// A title of the longest that may be, twice, and a count that does not fit in an int of 32 bits.
	big := strings.Repeat("b", content.MaxTextBytes)
	start := time.Now()
	got = p.titleText(strings.Repeat("%%title%% ", 3000), big, "Test")
	if want := (seoText{long: int64(3000)*content.MaxTextBytes + 2999}); !reflect.DeepEqual(got, want) {
		// The spaces between the titles are text too, and the one after the last is taken off.
		t.Errorf("titleText of 3000 titles of the longest = %+v, want %+v", got, want)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("titleText of 3000 titles of the longest took %v", d)
	}
	// The longest that may be is a title.
	exact := strings.Repeat("c", content.MaxTextBytes)
	if got := p.titleText(exact, "About", "Test"); got.text != exact || got.long != 0 {
		t.Errorf("a title of %d bytes: long = %d, text of %d bytes", len(exact), got.long, len(got.text))
	}
	if got := p.titleText(exact+"c", "About", "Test"); got.text != "" || got.long != content.MaxTextBytes+1 {
		t.Errorf("a title of %d bytes: long = %d, text of %d bytes", len(exact)+1, got.long, len(got.text))
	}
	// A text that NFC makes longer is checked after it: U+0344 has the 4 bytes of the two characters that it is made of.
	nfc := strings.Repeat(string(rune(0x344)), content.MaxTextBytes/2)
	if got := p.titleText(nfc, "About", "Test"); !reflect.DeepEqual(got, seoText{long: 2 * content.MaxTextBytes}) {
		t.Errorf("a title that NFC makes longer: %+v", got)
	}
}

func TestSEOSplit(t *testing.T) {
	piece := func(text, name string) seoPiece { return seoPiece{text: text, name: name} }
	for _, tt := range []struct {
		plugin, in string
		want       []seoPiece
	}{
		{"yoast", "", nil},
		{"yoast", "text", []seoPiece{piece("text", "")}},
		{"yoast", "%%a%%", []seoPiece{piece("%%a%%", "a")}},
		{"yoast", "x %%a%%%%b%% y", []seoPiece{piece("x ", ""), piece("%%a%%", "a"), piece("%%b%%", "b"), piece(" y", "")}},
		{"yoast", "%%a%b%%", []seoPiece{piece("%%a%b%%", "")}},
		{"yoast", "%%%a%%", []seoPiece{piece("%", ""), piece("%%a%%", "a")}},
		{"yoast", "%%a%%%", []seoPiece{piece("%%a%%", "a"), piece("%", "")}},
		{"yoast", "日本語%%日本%%語", []seoPiece{piece("日本語", ""), piece("%%日本%%", "日本"), piece("語", "")}},
		{"rankmath", "%a% %b(c d)%", []seoPiece{piece("%a%", "a"), piece(" ", ""), piece("%b(c d)%", "b(c d)")}},
		{"rankmath", "%a b%", []seoPiece{piece("%a b%", "")}},
		{"ssp", "%_a_%%_b_c_%", []seoPiece{piece("%_a_%", "a"), piece("%_b_c_%", "b_c")}},
		{"aioseo", "#a#b #custom_field-x-y, #c", []seoPiece{piece("#a", "a"), piece("#b", "b"), piece(" ", ""), piece("#custom_field-x-y", "custom_field-x-y"), piece(", ", ""), piece("#c", "c")}},
		{"tsf", "%%a%%", []seoPiece{piece("%%a%%", "")}},
	} {
		p := seoPluginNamed(tt.plugin)
		if got := p.vars.split(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("%s split %q = %+v, want %+v", tt.plugin, tt.in, got, tt.want)
		}
	}
}

func TestSEOTextIsLinear(t *testing.T) {
	// Values of the size of a large postmeta, made to make a scanner that starts again at each opener read them again and again,
	// or a loop that takes off the ends one at a time start again at the other end.
	const n = 1 << 19
	for _, tt := range []struct {
		name, plugin, value string
	}{
		{"percent signs", "yoast", strings.Repeat("%", n)},
		{"percent signs of the other form", "rankmath", strings.Repeat("%", n)},
		{"percent signs and underscores", "ssp", strings.Repeat("%_", n/2)},
		{"hashes", "aioseo", strings.Repeat("#", n)},
		{"an opener that is never closed", "yoast", "%%" + strings.Repeat("a", n)},
		{"an opener that is never closed, of the other form", "rankmath", "%" + strings.Repeat("a", n)},
		{"an opener that is never closed, with underscores", "ssp", "%_" + strings.Repeat("a", n)},
		{"openers with names", "yoast", strings.Repeat("%%a", n/3)},
		{"openers with arguments", "rankmath", strings.Repeat("%a(", n/3)},
		{"openers with names and signs", "rankmath", strings.Repeat("%a%", n/3)},
		{"openers with underscores", "ssp", strings.Repeat("%_a", n/3)},
		{"hashes with names", "aioseo", strings.Repeat("#a", n/2)},
		{"hashes of fields", "aioseo", strings.Repeat("#custom_field-", n/14)},
		{"variables that are not replaced", "yoast", strings.Repeat("%%a%%", n/5)},
		{"separators", "yoast", strings.Repeat("%%sep%%", n/7)},
		{"separators of the other form", "rankmath", strings.Repeat("%sep% ", n/6)},
		{"names of the site", "tsf", strings.Repeat("Test | ", n/7)},
		{"names of the site and spaces", "tsf", strings.Repeat("Test ", n/5)},
		{"titles", "yoast", strings.Repeat("%%title%%", n/9)},
		{"spaces", "tsf", strings.Repeat(" ", n)},
		{"signs that almost close", "yoast", strings.Repeat("%%a%b", n/5)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := seoPluginNamed(tt.plugin)
			start := time.Now()
			p.titleText(tt.value, "About", "Test")
			p.descriptionText(tt.value, "About", "Test")
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("%s took %v for %d bytes", tt.plugin, d, len(tt.value))
			}
		})
	}
}

func FuzzSEOText(f *testing.F) {
	for _, s := range []string{
		"", "text", "Custom title", "%%title%% %%page%% %%sep%% %%sitename%%", "%title% %sep% %sitename%", "%%post_title%%", "%_page_title_% %_sep_%",
		"#post_title #separator_sa #site_title", "Custom | Test", "Test - Custom - Test", "100% natural", "%%%%title%%%%", "%%", "%", "#", "%_", "%__%",
		"%customfield(price)% yen", "%a(%a(%a(", "%%a%b%%", "%%excerpt%%", "#custom_field-x-y", "#tax_name-", "Episode #1",
		"Line\nbreak\ttab", "e" + string(rune(0x301)), "日本語 | Test", "- – — | ｜ · • : ： / ／ ~ « » < > *", "%%cf_my field%%", "%%\n%%", "%a%b%c%",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if !utf8.ValidString(value) || len(value) > 1<<16 {
			t.Skip()
		}
		for i := range seoPlugins {
			p := &seoPlugins[i]
			for _, title := range []string{"About", "", "Test | About", "%%x%%"} {
				for name, got := range map[string]seoText{
					"title":       p.titleText(value, title, "Test"),
					"description": p.descriptionText(value, title, "Test"),
				} {
					if got.text != "" {
						if err := content.CheckLine(got.text); err != nil {
							t.Fatalf("%s %s of %q (title %q) is %q: %v", p.name, name, value, title, got.text, err)
						}
						if len(got.unknown) > 0 || got.long != 0 {
							t.Fatalf("%s %s of %q has text and more: %+v", p.name, name, value, got)
						}
						if vars := variables(p.vars.split(got.text)); len(vars) > 0 {
							t.Fatalf("%s %s of %q (title %q) is %q, which has the variables %q", p.name, name, value, title, got.text, vars)
						}
					}
					if len(got.unknown) > 0 && got.long != 0 {
						t.Fatalf("%s %s of %q has variables and a length: %+v", p.name, name, value, got)
					}
				}
			}
		}
	})
}

// seoSettings is entrySettings for a site that is called 見本サイト, with the SEO plugin.
func seoSettings(plugin string) *Settings {
	s := entrySettings()
	s.Site.Name = "見本サイト"
	s.SEO.Plugin = plugin
	return s
}

func metas(kv ...string) [][2]string {
	var out [][2]string
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, [2]string{kv[i], kv[i+1]})
	}
	return out
}

const seoUploads = "https://example.com/wp-content/uploads/"

// seoMedia are the attachments that the tests of the SEO settings use: the images 20 and 24, the PDF 21, and the image 22
// that is in the trash.
func seoMedia() []wxrEntry {
	attachment := func(id int, status, file string, meta ...string) wxrEntry {
		return wxrEntry{
			id: id, typ: "attachment", status: status, title: file, name: fmt.Sprintf("att-%d", id), link: fmt.Sprintf("https://example.com/att-%d/", id),
			attachmentURL: seoUploads + file, meta: append(metas("_wp_attached_file", file), metas(meta...)...),
		}
	}
	sizes := phpArray(phpString("sizes"), phpArray(phpString("medium"), phpArray(phpString("file"), phpString("a-300x200.png"))))
	return []wxrEntry{
		attachment(20, "inherit", "2026/10/a.png", "_wp_attachment_metadata", sizes),
		attachment(21, "inherit", "2026/10/b.pdf"),
		attachment(22, "trash", "2026/10/c.png"),
		attachment(24, "inherit", "2026/10/e.png"),
	}
}

func seoPost(id int, title string, kv ...string) wxrEntry {
	return wxrEntry{
		id: id, title: title, name: fmt.Sprintf("post-%d", id), link: fmt.Sprintf("https://example.com/post-%d/", id), meta: metas(kv...),
	}
}

// seoRun makes the entries of the items and the attachments of seoMedia, with the SEO plugin.
func seoRun(t *testing.T, plugin string, items ...wxrEntry) (*Import, *content.Site, []*EntryDoc) {
	t.Helper()
	return makeEntries(t, seoSettings(plugin), nil, entriesWXR("", append(seoMedia(), items...)...))
}

// seoSkipLines lists the entries of the report about the postmeta that are not imported, as "kind: message", and checks that
// each is about the channel, with no place in the documents.
func seoSkipLines(t *testing.T, imp *Import) []string {
	t.Helper()
	var out []string
	for _, e := range imp.Report.Entries() {
		if !strings.HasPrefix(e.Message, "the postmeta ") {
			continue
		}
		if e.Source.Record != recordChannel || e.Source.ID != 0 || e.Target != (Target{}) {
			t.Errorf("the entry %+v is not about the channel, with no place in the documents", e)
		}
		out = append(out, string(e.Kind)+": "+e.Message)
	}
	return out
}

func seoSkipLine(key string, items int) string {
	return fmt.Sprintf("skipped: the postmeta %q of %s is not imported; whiteroom has no setting for it", key, count(items, "item"))
}

func TestEntriesSEO(t *testing.T) {
	const rankRobots = `a:1:{i:0;s:7:"noindex";}`
	all := []string{
		"_yoast_wpseo_title", "Yoast title", "_yoast_wpseo_metadesc", "Yoast description", "_yoast_wpseo_canonical", "https://other.example.org/yoast/",
		"_yoast_wpseo_meta-robots-noindex", "1", "_yoast_wpseo_opengraph-image-id", "20",
		"rank_math_title", "Rank Math title", "rank_math_description", "Rank Math description", "rank_math_canonical_url", "https://other.example.org/rankmath/",
		"rank_math_robots", rankRobots, "rank_math_facebook_image_id", "20",
		"_seopress_titles_title", "SEOPress title", "_seopress_titles_desc", "SEOPress description", "_seopress_robots_canonical", "https://other.example.org/seopress/",
		"_seopress_robots_index", "yes", "_seopress_social_fb_img_attachment_id", "20",
		"_genesis_title", "TSF title", "_genesis_description", "TSF description", "_genesis_canonical_uri", "https://other.example.org/tsf/",
		"_genesis_noindex", "1", "_social_image_id", "24",
		"ssp_meta_title", "SSP title", "ssp_meta_description", "SSP description", "ssp_meta_canonical", "https://other.example.org/ssp/",
		"ssp_meta_robots", "noindex,nofollow", "ssp_meta_image", seoUploads + "2026/10/a.png",
		"_aioseo_title", "AIOSEO title", "_aioseo_description", "AIOSEO description",
	}
	for _, tt := range []struct {
		plugin string
		want   func(image20, image24 string) content.SEO
		skips  []string
	}{
		{"yoast", func(i20, _ string) content.SEO {
			return content.SEO{Title: "Yoast title", Description: "Yoast description", Canonical: "https://other.example.org/yoast/", NoIndex: true, OGImage: i20}
		}, nil},
		{"rankmath", func(i20, _ string) content.SEO {
			return content.SEO{Title: "Rank Math title", Description: "Rank Math description", Canonical: "https://other.example.org/rankmath/", NoIndex: true, OGImage: i20}
		}, nil},
		{"seopress", func(i20, _ string) content.SEO {
			return content.SEO{Title: "SEOPress title", Description: "SEOPress description", Canonical: "https://other.example.org/seopress/", NoIndex: true, OGImage: i20}
		}, nil},
		{"tsf", func(_, i24 string) content.SEO {
			return content.SEO{Title: "TSF title", Description: "TSF description", Canonical: "https://other.example.org/tsf/", NoIndex: true, OGImage: i24}
		}, nil},
		{"ssp", func(i20, _ string) content.SEO {
			return content.SEO{Title: "SSP title", Description: "SSP description", Canonical: "https://other.example.org/ssp/", NoIndex: true, OGImage: i20}
		}, []string{seoSkipLine("ssp_meta_robots", 1)}},
		{"aioseo", func(_, _ string) content.SEO {
			return content.SEO{Title: "AIOSEO title", Description: "AIOSEO description"}
		}, nil},
		{"", func(_, _ string) content.SEO { return content.SEO{} }, nil},
	} {
		t.Run(tt.plugin, func(t *testing.T) {
			imp, _, docs := seoRun(t, tt.plugin, seoPost(1, "About", all...))
			want := tt.want(postIDOf(t, imp, 20), postIDOf(t, imp, 24))
			if got := docOf(docs, 1).Revision.SEO; got != want {
				t.Errorf("seo = %+v, want %+v", got, want)
			}
			sameSet(t, "the postmeta that are not imported", seoSkipLines(t, imp), tt.skips)
			wantItem(t, imp, docs, 1)
		})
	}

	// Entries is given settings that Site did not check, or none.
	for name, s := range map[string]*Settings{"no settings": nil, "a plugin that is not one": seoSettings("nosuch")} {
		t.Run(name, func(t *testing.T) {
			in := entriesWXR("", append(seoMedia(), seoPost(1, "About", all...))...)
			imp, site := planEntries(t, seoSettings("yoast"), nil, in)
			docs, err := collectEntries(t.Context(), imp, s, site)
			if err != nil {
				t.Fatal(err)
			}
			checkEntries(t, imp, site, docs)
			if got := docOf(docs, 1).Revision.SEO; got != (content.SEO{}) {
				t.Errorf("seo = %+v, want none", got)
			}
			sameSet(t, "the postmeta that are not imported", seoSkipLines(t, imp), nil)
		})
	}
	t.Run("every kind of entry has it", func(t *testing.T) {
		imp, _, docs := seoRun(t, "yoast",
			wxrEntry{id: 1, typ: "page", title: "Page", name: "page", meta: metas("_yoast_wpseo_title", "Page title")},
			wxrEntry{id: 2, typ: "works", title: "Work", name: "work", meta: metas("_yoast_wpseo_metadesc", "Work description")},
			wxrEntry{id: 3, status: "draft", title: "Draft", name: "draft", meta: metas("_yoast_wpseo_meta-robots-noindex", "1")},
			wxrEntry{id: 4, status: "private", title: "Private", name: "private", meta: metas("_yoast_wpseo_title", "Private title")},
		)
		for post, want := range map[int64]content.SEO{
			1: {Title: "Page title"}, 2: {Description: "Work description"}, 3: {NoIndex: true}, 4: {Title: "Private title"},
		} {
			if got := docOf(docs, post).Revision.SEO; got != want {
				t.Errorf("the seo of item %d is %+v, want %+v", post, got, want)
			}
		}
		_ = imp
	})
	t.Run("the values are the first postmeta, without the white space around them", func(t *testing.T) {
		_, _, docs := seoRun(t, "yoast",
			seoPost(1, "About", "_yoast_wpseo_title", " \n Spaced title \t", "_yoast_wpseo_title", "Second"),
			seoPost(2, "About", "_yoast_wpseo_title", "", "_yoast_wpseo_title", "Later one"),
			seoPost(3, "About", "_yoast_wpseo_title", " \n ", "_yoast_wpseo_metadesc", "  Desc  "),
		)
		for post, want := range map[int64]content.SEO{1: {Title: "Spaced title"}, 2: {}, 3: {Description: "Desc"}} {
			if got := docOf(docs, post).Revision.SEO; got != want {
				t.Errorf("the seo of item %d is %+v, want %+v", post, got, want)
			}
		}
	})
	t.Run("the same output each time", func(t *testing.T) {
		run := func() string {
			imp, _, docs := seoRun(t, "yoast",
				seoPost(1, "About", all...), seoPost(2, "Two", "_yoast_wpseo_opengraph-title", "x", "_yoast_wpseo_title", "%%category%%"),
				seoPost(3, "Three", "_yoast_wpseo_twitter-title", "y", "_yoast_wpseo_opengraph-title", "z"))
			return entriesText(t, imp, docs)
		}
		first := run()
		for range 5 {
			if got := run(); got != first {
				t.Fatal("the output changed between two runs")
			}
		}
	})
}

func TestEntriesSEONoindex(t *testing.T) {
	notList := func(key string) string {
		return fmt.Sprintf("skipped: the noindex setting is not imported: the value of %q is not a list -> {R}#/seo/noindex", key)
	}
	for _, tt := range []struct {
		plugin, key string
		rows        []struct {
			value string
			want  bool
			lines []string // besides the one about the postmeta that are not imported
		}
	}{
		{"yoast", "_yoast_wpseo_meta-robots-noindex", []struct {
			value string
			want  bool
			lines []string
		}{
			{"1", true, nil}, {" 1 \n", true, nil}, {"0", false, nil}, {"2", false, nil}, {"", false, nil}, {"-1", false, nil}, {"11", false, nil},
			{"noindex", false, nil}, {"true", false, nil},
		}},
		{"tsf", "_genesis_noindex", []struct {
			value string
			want  bool
			lines []string
		}{
			{"1", true, nil}, {"-1", false, nil}, {"0", false, nil}, {"", false, nil}, {"yes", false, nil},
		}},
		{"seopress", "_seopress_robots_index", []struct {
			value string
			want  bool
			lines []string
		}{
			{"yes", true, nil}, {" yes ", true, nil}, {"no", false, nil}, {"", false, nil}, {"1", false, nil}, {"Yes", false, nil}, {"noindex", false, nil},
		}},
		{"ssp", "ssp_meta_robots", []struct {
			value string
			want  bool
			lines []string
		}{
			{"noindex", true, nil}, {"noindex,nofollow", true, nil}, {"noindex,follow", true, nil}, {"nofollow, noindex", true, nil},
			{" NoIndex ", true, nil}, {"index,follow", false, nil}, {"nofollow", false, nil}, {"noarchive", false, nil}, {"", false, nil},
			{"noindexed", false, nil}, {"no index", false, nil},
		}},
		{"rankmath", "rank_math_robots", []struct {
			value string
			want  bool
			lines []string
		}{
			{`a:1:{i:0;s:7:"noindex";}`, true, nil},
			{`a:2:{i:0;s:7:"noindex";i:1;s:8:"nofollow";}`, true, nil},
			{`a:2:{i:0;s:8:"nofollow";i:1;s:7:"noindex";}`, true, nil},
			{`a:1:{i:0;s:7:"NOINDEX";}`, true, nil},
			{`a:1:{i:0;s:5:"index";}`, false, nil},
			{`a:1:{i:0;s:8:"nofollow";}`, false, nil},
			{`a:0:{}`, false, nil},
			{`a:1:{i:0;i:1;}`, false, nil},
			{`a:1:{s:7:"noindex";b:1;}`, false, nil},
			{`not serialized`, false, []string{notList("rank_math_robots")}},
			{`noindex`, false, []string{notList("rank_math_robots")}},
			{`s:7:"noindex";`, false, []string{notList("rank_math_robots")}},
			{`i:1;`, false, []string{notList("rank_math_robots")}},
			{`a:1:{i:0;s:7:"noindex";`, false, []string{`skipped: the noindex setting is not imported: the value of "rank_math_robots": byte 23: unexpected end of the value -> {R}#/seo/noindex`}},
			{`a:1:{i:0;s:7:"noindex"`, false, []string{notList("rank_math_robots")}},
		}},
	} {
		t.Run(tt.plugin, func(t *testing.T) {
			var items []wxrEntry
			for i, r := range tt.rows {
				items = append(items, seoPost(i+1, "About", tt.key, r.value))
			}
			imp, _, docs := seoRun(t, tt.plugin, items...)
			for i, r := range tt.rows {
				post := int64(i + 1)
				if got := docOf(docs, post).Revision.SEO.NoIndex; got != r.want {
					t.Errorf("noindex of %q = %v, want %v", r.value, got, r.want)
				}
				// The record of the postmeta that are not imported is not about the item, so these are all of it.
				wantItem(t, imp, docs, post, r.lines...)
			}
		})
	}
}

func TestEntriesSEOTitle(t *testing.T) {
	const unknown = `review: the title for search engines %s is not imported: the import does not replace %s -> {R}#/seo/title`
	const unknownDesc = `review: the description for search engines %s is not imported: the import does not replace %s -> {R}#/seo/description`
	long := strings.Repeat("x", 100)
	for _, tt := range []struct {
		name   string
		plugin string
		title  string // of the entry
		status string
		meta   []string
		want   content.SEO
		lines  []string
	}{
		{name: "the format of the title", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%title%% %%page%% %%sep%% %%sitename%%"}},
		{name: "the same title and the name of the site that is written", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "会社概要 | 見本サイト"}},
		{name: "another title and the name of the site that is written", plugin: "yoast", title: "会社概要",
			meta: []string{"_yoast_wpseo_title", "私たちについて | 見本サイト"}, want: content.SEO{Title: "私たちについて"}},
		{name: "the name of the site, which has no place here", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "見本サイト"}},
		{name: "a title that no one else has", plugin: "rankmath", title: "会社概要", meta: []string{"rank_math_title", "%title% - Custom %sep% %sitename%"},
			want: content.SEO{Title: "会社概要 - Custom"}},
		{name: "the title that is empty", plugin: "yoast", status: "draft", title: "", meta: []string{"_yoast_wpseo_title", "%%title%% %%sep%% %%sitename%%"}},
		{name: "the title that is empty, with text", plugin: "yoast", status: "draft", title: "", meta: []string{"_yoast_wpseo_title", "Custom %%title%%"},
			want: content.SEO{Title: "Custom"}},
		{name: "a variable that is not replaced", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%category%% news"},
			lines: []string{fmt.Sprintf(unknown, `"%%category%% news"`, "%%category%%")}},
		{name: "variables, once each", plugin: "seopress", title: "会社概要", meta: []string{"_seopress_titles_title", "%%post_title%% %%post_category%% %%post_tag%% %%post_category%%"},
			lines: []string{fmt.Sprintf(unknown, `"%%post_title%% %%post_category%% %%post_tag%% %%post_category%%"`, "%%post_category%%, %%post_tag%%")}},
		{name: "a variable with an argument", plugin: "rankmath", title: "会社概要", meta: []string{"rank_math_title", "%customfield(my price)% yen"},
			lines: []string{fmt.Sprintf(unknown, `"%customfield(my price)% yen"`, `"%customfield(my price)%"`)}},
		{name: "a variable that is not ASCII is quoted", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%日本語%% x"},
			lines: []string{fmt.Sprintf(unknown, `"%%日本語%% x"`, `"%%日本語%%"`)}},
		{name: "a variable with a space is quoted", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%cf_my field%% x"},
			lines: []string{fmt.Sprintf(unknown, `"%%cf_my field%% x"`, `"%%cf_my field%%"`)}},
		{name: "a hash", plugin: "aioseo", title: "会社概要", meta: []string{"_aioseo_title", "#post_title #author_name"},
			lines: []string{fmt.Sprintf(unknown, `"#post_title #author_name"`, "#author_name")}},
		{name: "a value that is long is cut", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%category%% " + long + long},
			lines: []string{fmt.Sprintf(unknown, `"%%category%% `+long[:87]+`..."`, "%%category%%")}},
		{name: "a value of the length that is quoted whole", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%category%%" + strings.Repeat("x", 88)},
			lines: []string{fmt.Sprintf(unknown, `"%%category%%`+strings.Repeat("x", 88)+`"`, "%%category%%")}},
		{name: "a value that is one byte too long", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%category%%" + strings.Repeat("x", 89)},
			lines: []string{fmt.Sprintf(unknown, `"%%category%%`+strings.Repeat("x", 88)+`..."`, "%%category%%")}},
		{name: "a variable that is long is cut", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%" + strings.Repeat("x", 50) + "%% y"},
			lines: []string{fmt.Sprintf(unknown, `"%%`+strings.Repeat("x", 50)+`%% y"`, `"%%`+strings.Repeat("x", 38)+`..."`)}},
		{name: "a variable with a character that has no place in a name is quoted", plugin: "yoast", title: "会社概要",
			meta:  []string{"_yoast_wpseo_title", "%%a" + string(rune(0x7f)) + "b%% x"},
			lines: []string{fmt.Sprintf(unknown, `"%%a\x7fb%% x"`, `"%%a\x7fb%%"`)}},
		{name: "a variable with a quote in it is quoted", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", `%%a"b%% x`},
			lines: []string{fmt.Sprintf(unknown, `"%%a\"b%% x"`, `"%%a\"b%%"`)}},
		{name: "a variable with a backslash in it is quoted", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", `%%a\b%% x`},
			lines: []string{fmt.Sprintf(unknown, `"%%a\\b%% x"`, `"%%a\\b%%"`)}},
		{name: "a value that is long, and cut in a character", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%category%%" + strings.Repeat("あ", 100)},
			lines: []string{fmt.Sprintf(unknown, `"%%category%%`+strings.Repeat("あ", 29)+`..."`, "%%category%%")}},
		{name: "a line break in the value is in the quotation", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_title", "%%category%%\nx"},
			lines: []string{fmt.Sprintf(unknown, `"%%category%%\nx"`, "%%category%%")}},
		{name: "too many variables", plugin: "yoast", title: "会社概要",
			meta: []string{"_yoast_wpseo_title", "%%a%% %%b%% %%c%% %%d%% %%e%% %%f%% %%g%% %%h%% %%i%% %%j%% %%k%% %%l%%"},
			lines: []string{fmt.Sprintf(unknown, `"%%a%% %%b%% %%c%% %%d%% %%e%% %%f%% %%g%% %%h%% %%i%% %%j%% %%k%% %%l%%"`,
				"%%a%%, %%b%%, %%c%%, %%d%%, %%e%%, %%f%%, %%g%%, %%h%%, %%i%%, %%j%%, and 2 more")}},
		{name: "too long", plugin: "yoast", title: long, meta: []string{"_yoast_wpseo_title", strings.Repeat("%%title%%", 20000)},
			lines: []string{fmt.Sprintf("skipped: the title for search engines is not imported: it is too long: %d bytes (max %d) -> {R}#/seo/title", 20000*100, content.MaxTextBytes)}},
		{name: "a description with the title and the name of the site", plugin: "yoast", title: "会社概要",
			meta: []string{"_yoast_wpseo_metadesc", "%%title%%は%%sitename%%の会社です"}, want: content.SEO{Description: "会社概要は見本サイトの会社です"}},
		{name: "a description that is the excerpt", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_metadesc", "%%excerpt%%"}},
		{name: "a description with a variable that is not replaced", plugin: "rankmath", title: "会社概要",
			meta:  []string{"rank_math_description", "%excerpt% %category%"},
			lines: []string{fmt.Sprintf(unknownDesc, `"%excerpt% %category%"`, "%excerpt%, %category%")}},
		{name: "a description in lines", plugin: "yoast", title: "会社概要", meta: []string{"_yoast_wpseo_metadesc", "One\r\nTwo\tThree"},
			want: content.SEO{Description: "One Two Three"}},
		{name: "a description too long", plugin: "ssp", title: long, meta: []string{"ssp_meta_description", strings.Repeat("%_page_title_%", 20000)},
			lines: []string{fmt.Sprintf("skipped: the description for search engines is not imported: it is too long: %d bytes (max %d) -> {R}#/seo/description", 20000*100, content.MaxTextBytes)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, _, docs := seoRun(t, tt.plugin, wxrEntry{id: 1, status: tt.status, title: tt.title, name: "one", meta: metas(tt.meta...)})
			if got := docOf(docs, 1).Revision.SEO; got != tt.want {
				t.Errorf("seo = %+v, want %+v", got, tt.want)
			}
			wantItem(t, imp, docs, 1, tt.lines...)
		})
	}

	t.Run("a record is about the postmeta", func(t *testing.T) {
		imp, _, _ := seoRun(t, "yoast",
			seoPost(1, "A", "_yoast_wpseo_focuskw", "k", "_yoast_wpseo_title", "%%x%%", "_yoast_wpseo_metadesc", "%%y%%"))
		wantMetaSource(t, imp, 1, "_yoast_wpseo_title", "the title for search engines")
		wantMetaSource(t, imp, 1, "_yoast_wpseo_metadesc", "the description for search engines")
	})
}

func TestEntriesSEOCanonical(t *testing.T) {
	const (
		notAbsolute = `must be an absolute URL or a path starting with "/"`
		uploads     = seoUploads
	)
	skipped := func(msg string) string {
		return "skipped: the canonical URL is not imported: " + msg + " -> {R}#/seo/canonical"
	}
	big := "https://example.com/" + strings.Repeat("a", content.MaxTextBytes)
	posts := []wxrEntry{
		{id: 1, title: "Hello", name: "hello", link: "https://example.com/hello/"},
		{id: 2, title: "Two", name: "two", link: "https://example.com/two/"},
		{id: 3, typ: "page", title: "Plain", name: "plain", link: "https://example.com/?page_id=3"},
		{id: 4, title: "日本", name: "nihon", link: "https://example.com/%E6%97%A5%E6%9C%AC/"},
		{id: 5, title: "Five", name: "five", link: "https://example.com/five/"},
	}
	canonical := func(id int, value string) wxrEntry {
		x := wxrEntry{id: id, title: fmt.Sprintf("Item %d", id), name: fmt.Sprintf("item-%d", id), link: fmt.Sprintf("https://example.com/item-%d/", id)}
		x.meta = metas("_yoast_wpseo_canonical", value)
		return x
	}
	own := func(id int, link, value string) wxrEntry {
		x := canonical(id, value)
		x.link = link
		return x
	}
	tests := []struct {
		item  wxrEntry
		want  string
		lines []string
	}{
		{canonical(10, "https://other.example.org/x/"), "https://other.example.org/x/", nil},
		{canonical(11, "https://example.com/hello/"), "/hello/", nil},
		{canonical(12, "https://example.com/hello"), "/hello", nil},
		{canonical(13, "https://EXAMPLE.com:443/hello/#frag"), "/hello/", nil},
		{canonical(14, "http://example.com/hello/"), "/hello/", nil},
		{canonical(15, "https://example.com/hello/?utm=1&b=2"), "/hello/?utm=1&b=2", nil},
		{canonical(16, "https://example.com/"), "/", nil},
		{canonical(17, "https://example.com//hello//"), "/hello/", nil},
		{canonical(18, "https://example.com/nothing/here/"), "/nothing/here/", nil},
		{canonical(19, "https://example.com/日本/"), "/日本/", nil},
		{canonical(74, "https://example.com/%E6%97%A5%E6%9C%AC/"), "/日本/", nil},
		{own(86, "https://example.com/dup2/", "https://example.com/dup2/"), "", nil},
		{own(87, "https://example.com//dup2//", "https://example.com/dup2/"), "", nil},
		{canonical(90, "https://example.com/a%09b/"), "/a%09b/", nil},
		{canonical(70, "  https://example.com/two/ \n"), "/two/", nil},
		{canonical(71, "https://example.com/?p=1"), "/?p=1", nil},
		{canonical(72, "/hello/"), "/hello/", nil},
		{canonical(73, "https://example.com/wp-content/uploads/2026/10/a.png"), "/wp-content/uploads/2026/10/a.png", nil},
		{own(30, "https://example.com/own/", "https://example.com/own/"), "", nil},
		{own(31, "https://example.com/own", "https://example.com/own/"), "/own/", nil},
		{own(32, "https://example.com/own/", "https://example.com/own"), "/own", nil},
		{own(33, "https://example.com/own/", "https://example.com//own//"), "", nil},
		{own(34, "https://example.com/own/", "http://Example.com:80/own/"), "", nil},
		{own(35, "https://example.com/own/", "https://example.com/own/#top"), "", nil},
		{own(36, "https://example.com/own/", "https://example.com/own/?x=1"), "/own/?x=1", nil},
		{own(37, "https://example.com/own/", "https://example.com/?p=37"), "", nil},
		{own(38, "https://example.com/?p=38", "https://example.com/?p=38"), "", nil},
		{own(39, "https://example.com/?p=39", "https://example.com/own-39/"), "/own-39/", nil},
		{own(40, "https://example.com/%E6%97%A5%E6%9C%AC40/", "https://example.com/日本40/"), "", nil},
		{own(41, "https://example.com/%E6%97%A5%E6%9C%AC41/", "https://example.com/%e6%97%a5%e6%9c%ac41/"), "", nil},
		{own(42, "https://other.example.org/own/", "https://other.example.org/own/"), "https://other.example.org/own/", nil},
		{own(43, "https://example.com/hello/", "https://example.com/hello/"), "", nil},
		{own(80, "https://example.com//dup//", "https://example.com/dup/"), "", nil},
		{canonical(81, "https://exa"+string(rune(9))+"mple.com/two/"), "/two/", nil},
		{own(82, "https://example.com/uniq/", "https://example.com/uniq/?x=1"), "/uniq/?x=1", nil},
		{own(83, "https://example.com/uniq83/", "https://example.com/?p=83&x=1"), "", nil},
		{own(84, "https://example.com/uniq84/", "https://example.com/index.php?p=84"), "", nil},
		{own(85, "https://example.com/uniq85/", "https://example.com/uniq85/?p=85"), "/uniq85/?p=85", nil},
		{canonical(50, "javascript:alert(1)"), "", []string{skipped(`scheme "javascript" is not allowed`)}},
		{canonical(51, "relative/path"), "", []string{skipped(notAbsolute)}},
		{canonical(52, "mailto:a@example.com"), "", []string{skipped(`scheme "mailto" is not allowed for canonical`)}},
		{canonical(53, "tel:+81312345678"), "", []string{skipped(`scheme "tel" is not allowed for canonical`)}},
		{canonical(54, "//example.com/x/"), "", []string{skipped("protocol-relative URLs are not allowed")}},
		{canonical(55, `https://example.com\evil/`), "", []string{skipped("backslashes are not allowed")}},
		{canonical(56, "ftp://example.com/x"), "", []string{skipped(`scheme "ftp" is not allowed`)}},
		{canonical(57, "https:///x"), "", []string{skipped("missing host")}},
		{canonical(58, "https://user:pw@other.example.org/x"), "", []string{skipped("credentials in URLs are not allowed")}},
		{canonical(59, "http://[::1"), "", []string{skipped("invalid URL")}},
		{canonical(60, big), "", []string{skipped(fmt.Sprintf("too long: %d bytes (max %d)", len(big), content.MaxTextBytes))}},
		{canonical(61, "x"), "", []string{skipped(notAbsolute)}},
	}
	items := append([]wxrEntry{}, posts...)
	for _, tt := range tests {
		items = append(items, tt.item)
	}
	imp, _, docs := seoRun(t, "yoast", items...)
	for _, tt := range tests {
		id := int64(tt.item.id)
		t.Run(fmt.Sprintf("%d", id), func(t *testing.T) {
			if got := docOf(docs, id).Revision.SEO.Canonical; got != tt.want {
				t.Errorf("the canonical URL of %q is %q, want %q", tt.item.meta[0][1], got, tt.want)
			}
			wantItem(t, imp, docs, id, tt.lines...)
		})
	}
	_ = uploads

	t.Run("a site in a directory", func(t *testing.T) {
		in := strings.Replace(entriesWXR("",
			wxrEntry{id: 1, title: "One", name: "one", link: "https://example.com/blog/one/"},
			canonicalOf(2, "https://example.com/blog/one/"),
			canonicalOf(3, "/blog/one/"),
			canonicalOf(4, "/one/"),
			canonicalOf(5, "https://example.com/other/"),
			canonicalOf(6, "https://example.com/blog/"),
			canonicalOf(7, "https://example.com/blog"),
			canonicalOf(8, "https://example.com/blog/x/?y=1"),
			canonicalOf(9, "/blog/"),
			canonicalOf(10, "/"),
		), "<link>https://example.com</link>", "<link>https://example.com/blog</link>", 1)
		imp, site := planEntries(t, seoSettings("yoast"), nil, in)
		docs, err := collectEntries(t.Context(), imp, seoSettings("yoast"), site)
		if err != nil {
			t.Fatal(err)
		}
		checkEntries(t, imp, site, docs)
		for post, want := range map[int64]string{
			1: "", 2: "/one/", 3: "/one/", 4: "https://example.com/one/", 5: "https://example.com/other/", 6: "/", 7: "/",
			8: "/x/?y=1", 9: "/", 10: "https://example.com/",
		} {
			if got := docOf(docs, post).Revision.SEO.Canonical; got != want {
				t.Errorf("the canonical URL of item %d is %q, want %q", post, got, want)
			}
		}
	})
	t.Run("a site that has no usable home keeps the addresses", func(t *testing.T) {
		in := strings.Replace(entriesWXR("", canonicalOf(1, "https://example.com/a/"), canonicalOf(2, "/b/")),
			"<link>https://example.com</link>", "<link>not a url</link>", 1)
		imp, site := planEntries(t, seoSettings("yoast"), nil, in)
		docs, err := collectEntries(t.Context(), imp, seoSettings("yoast"), site)
		if err != nil {
			t.Fatal(err)
		}
		checkEntries(t, imp, site, docs)
		for post, want := range map[int64]string{1: "https://example.com/a/", 2: "/b/"} {
			if got := docOf(docs, post).Revision.SEO.Canonical; got != want {
				t.Errorf("the canonical URL of item %d is %q, want %q", post, got, want)
			}
		}
	})
}

// canonicalOf is a post whose canonical URL of Yoast SEO is value.
func canonicalOf(id int, value string) wxrEntry {
	return wxrEntry{
		id: id, title: fmt.Sprintf("Item %d", id), name: fmt.Sprintf("item-%d", id), link: fmt.Sprintf("https://example.com/blog/item-%d/", id),
		meta: metas("_yoast_wpseo_canonical", value),
	}
}

func TestEntriesSEOImage(t *testing.T) {
	const idKey, urlKey = "_yoast_wpseo_opengraph-image-id", "_yoast_wpseo_opengraph-image"
	notImported := func(n int) string {
		return fmt.Sprintf("skipped: the image for sharing, attachment %d, is not imported -> {R}#/seo/ogImage", n)
	}
	notAnImage := func(n int) string {
		return fmt.Sprintf("skipped: the image for sharing, attachment %d, is not an image -> {R}#/seo/ogImage", n)
	}
	notAFile := func(url string) string {
		return fmt.Sprintf("skipped: the image for sharing %q is not a file of an attachment of the WXR -> {R}#/seo/ogImage", url)
	}
	const (
		a  = seoUploads + "2026/10/a.png"
		e  = seoUploads + "2026/10/e.png"
		b  = seoUploads + "2026/10/b.pdf"
		c  = seoUploads + "2026/10/c.png"
		a3 = seoUploads + "2026/10/a-300x200.png"
	)
	rows := []struct {
		meta     []string
		featured string // the _thumbnail_id
		want     string // the post of the attachment of the image: "20", "24" or ""
		lines    []string
	}{
		{meta: []string{idKey, "20"}, want: "20"},
		{meta: []string{idKey, " 20 \n"}, want: "20"},
		{meta: []string{idKey, "24"}, want: "24"},
		{meta: []string{idKey, "21"}, lines: []string{notAnImage(21)}},
		{meta: []string{idKey, "22"}, lines: []string{notImported(22)}},
		{meta: []string{idKey, "99"}, lines: []string{notImported(99)}},
		{meta: []string{idKey, "23"}, lines: []string{notImported(23)}},
		{meta: []string{idKey, "0", urlKey, a}, want: "20"},
		{meta: []string{idKey, "abc", urlKey, a}, want: "20"},
		{meta: []string{idKey, "-1", urlKey, a}, want: "20"},
		{meta: []string{idKey, "2.5", urlKey, e}, want: "24"},
		{meta: []string{idKey, "+20", urlKey, e}, want: "24"},
		{meta: []string{idKey, "99999999999999999999", urlKey, a}, want: "20"},
		{meta: []string{idKey, "", urlKey, a}, want: "20"},
		{meta: []string{idKey, "20", urlKey, e}, want: "20"},
		{meta: []string{idKey, "99", urlKey, a}, lines: []string{notImported(99)}},
		{meta: []string{idKey, "21", urlKey, a}, lines: []string{notAnImage(21)}},
		{meta: []string{idKey, "0"}},
		{meta: []string{idKey, "abc"}},
		{meta: []string{urlKey, a}, want: "20"},
		{meta: []string{urlKey, "  " + a + "\n"}, want: "20"},
		{meta: []string{urlKey, a3}, want: "20"},
		{meta: []string{urlKey, "https://example.com/wp-content/uploads/2026/10/a" + string(rune(9)) + ".png"}, want: "20"},
		{meta: []string{urlKey, strings.Replace(a, "https:", "http:", 1)}, want: "20"},
		{meta: []string{urlKey, a + "?ver=2"}, want: "20"},
		{meta: []string{urlKey, "https://example.com/att-20/"}, want: "20"},
		{meta: []string{urlKey, "/wp-content/uploads/2026/10/e.png"}, want: "24"},
		{meta: []string{urlKey, e}, want: "24"},
		{meta: []string{urlKey, "https://cdn.example.org/a.png"}, lines: []string{notAFile("https://cdn.example.org/a.png")}},
		{meta: []string{urlKey, "https://example.com/post-1/"}, lines: []string{notAFile("https://example.com/post-1/")}},
		{meta: []string{urlKey, "/wp-content/uploads/2026/10/none.png"}, lines: []string{notAFile("/wp-content/uploads/2026/10/none.png")}},
		{meta: []string{urlKey, c}, lines: []string{notAFile(c)}},
		{meta: []string{urlKey, b}, lines: []string{notAnImage(21)}},
		{meta: []string{urlKey, "not a url"}, lines: []string{notAFile("not a url")}},
		{meta: []string{urlKey, "%zz"}, lines: []string{notAFile("%zz")}},
		{meta: []string{urlKey, "data:image/png;base64,AAAA"}, lines: []string{notAFile("data:image/png;base64,AAAA")}},
		{meta: []string{urlKey, "//example.com/wp-content/uploads/2026/10/a.png"}, lines: []string{notAFile("//example.com/wp-content/uploads/2026/10/a.png")}},
		{meta: []string{urlKey, "https://example.com/?attachment_id=20"}, want: "20"},
		{meta: []string{urlKey, "https://example.com/?p=1"}, lines: []string{notAFile("https://example.com/?p=1")}},
		{meta: []string{urlKey, strings.Repeat("x", 300)}, lines: []string{notAFile(strings.Repeat("x", 100) + "...")}},
		{meta: []string{idKey, "20"}, featured: "20"},
		{meta: []string{urlKey, a}, featured: "20"},
		{meta: []string{idKey, "24"}, featured: "20", want: "24"},
		{meta: []string{idKey, "20"}, featured: "24", want: "20"},
		{meta: []string{idKey, "20"}, featured: "21", want: "20", lines: []string{"skipped: the featured image, attachment 21, is not an image -> {R}#/featuredImage"}},
		{meta: []string{idKey, "21"}, featured: "21", lines: []string{
			"skipped: the featured image, attachment 21, is not an image -> {R}#/featuredImage", notAnImage(21)}},
	}
	var items []wxrEntry
	items = append(items, wxrEntry{id: 1, title: "Hello", name: "hello", link: "https://example.com/post-1/"})
	items = append(items, wxrEntry{id: 23, typ: "attachment", status: "inherit", title: "d", name: "att-23", link: "https://example.com/att-23/",
		attachmentURL: seoUploads + "2026/10/d.png", meta: metas("_wp_attached_file", "2026/10/d.png")})
	for i, r := range rows {
		x := seoPost(100+i, fmt.Sprintf("Row %d", i), r.meta...)
		if r.featured != "" {
			x.meta = append(x.meta, [2]string{"_thumbnail_id", r.featured})
		}
		items = append(items, x)
	}
	in := entriesWXR("", append(seoMedia(), items...)...)
	imp, site := planEntries(t, seoSettings("yoast"), nil, in)
	_, id23, _ := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: 23})
	imp.DropRef(id23)
	docs, err := collectEntries(t.Context(), imp, seoSettings("yoast"), site)
	if err != nil {
		t.Fatal(err)
	}
	checkEntries(t, imp, site, docs)
	for i, r := range rows {
		post := int64(100 + i)
		want := ""
		if r.want != "" {
			want = postIDOf(t, imp, map[string]int64{"20": 20, "24": 24}[r.want])
		}
		if got := docOf(docs, post).Revision.SEO.OGImage; got != want {
			t.Errorf("row %d (%q): the image for sharing is %q, want %q (%s)", i, r.meta, got, want, r.want)
		}
		wantItem(t, imp, docs, post, r.lines...)
	}
	// The records are about the postmeta that was read.
	find := func(key, value string) int64 {
		for i, r := range rows {
			if len(r.meta) == 2 && r.meta[0] == key && r.meta[1] == value && r.featured == "" {
				return int64(100 + i)
			}
		}
		t.Fatalf("no row of %s with the value %q", key, value)
		return 0
	}
	wantMetaSource(t, imp, find(idKey, "99"), idKey, "the image for sharing")
	wantMetaSource(t, imp, find(idKey, "21"), idKey, "the image for sharing")
	wantMetaSource(t, imp, find(urlKey, "https://cdn.example.org/a.png"), urlKey, "the image for sharing")
}

func TestEntriesSEOImageOfEachPlugin(t *testing.T) {
	a, e := seoUploads+"2026/10/a.png", seoUploads+"2026/10/e.png"
	for _, tt := range []struct {
		plugin, idKey, urlKey string
	}{
		{"yoast", "_yoast_wpseo_opengraph-image-id", "_yoast_wpseo_opengraph-image"},
		{"rankmath", "rank_math_facebook_image_id", "rank_math_facebook_image"},
		{"seopress", "_seopress_social_fb_img_attachment_id", "_seopress_social_fb_img"},
		{"tsf", "_social_image_id", "_social_image_url"},
		{"ssp", "", "ssp_meta_image"},
	} {
		t.Run(tt.plugin, func(t *testing.T) {
			items := []wxrEntry{seoPost(1, "By URL", tt.urlKey, e), seoPost(2, "By nothing")}
			if tt.idKey != "" {
				items = append(items, seoPost(3, "By ID", tt.idKey, "20"), seoPost(4, "Both", tt.idKey, "20", tt.urlKey, e))
			}
			imp, _, docs := seoRun(t, tt.plugin, items...)
			i20, i24 := postIDOf(t, imp, 20), postIDOf(t, imp, 24)
			want := map[int64]string{1: i24, 2: "", 3: i20, 4: i20}
			for _, d := range docs {
				if w, ok := want[d.Post]; ok && d.Revision.SEO.OGImage != w {
					t.Errorf("the image for sharing of item %d is %q, want %q", d.Post, d.Revision.SEO.OGImage, w)
				}
			}
		})
	}
	// An image that the plugin has no key for is not read, and a key of another plugin is not.
	for _, plugin := range []string{"aioseo", "ssp", "tsf", "rankmath"} {
		imp, _, docs := seoRun(t, plugin, seoPost(1, "Yoast", "_yoast_wpseo_opengraph-image-id", "20", "_yoast_wpseo_opengraph-image", a))
		if got := docOf(docs, 1).Revision.SEO; got != (content.SEO{}) {
			t.Errorf("%s read the postmeta of Yoast SEO: %+v", plugin, got)
		}
		wantItem(t, imp, docs, 1)
	}
	imp, _, docs := seoRun(t, "aioseo", seoPost(1, "Others", "_social_image_id", "20", "ssp_meta_image", a, "rank_math_facebook_image", a))
	if got := docOf(docs, 1).Revision.SEO.OGImage; got != "" {
		t.Errorf("All in One SEO has the image %q", got)
	}
	wantItem(t, imp, docs, 1)
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestEntriesSEONotImported(t *testing.T) {
	type row struct {
		key, value string
		counted    bool
	}
	var rankRobots = func(words ...string) string {
		var pairs []string
		for i, w := range words {
			pairs = append(pairs, phpInt(i), phpString(w))
		}
		return phpArray(pairs...)
	}
	for _, tt := range []struct {
		plugin string
		rows   []row
	}{
		{"yoast", []row{
			{"_yoast_wpseo_opengraph-title", "x", true}, {"_yoast_wpseo_opengraph-description", "x", true}, {"_yoast_wpseo_twitter-title", "x", true},
			{"_yoast_wpseo_twitter-description", "x", true}, {"_yoast_wpseo_twitter-image", "https://example.com/x.png", true},
			{"_yoast_wpseo_meta-robots-adv", "noimageindex,noarchive", true}, {"_yoast_wpseo_bctitle", "x", true},
			{"_yoast_wpseo_redirect", "https://example.com/x/", true},
			{"_yoast_wpseo_meta-robots-nofollow", "1", true}, {"_yoast_wpseo_meta-robots-nofollow", "0", false}, {"_yoast_wpseo_meta-robots-nofollow", "2", false},
			{"_yoast_wpseo_opengraph-title", "", false}, {"_yoast_wpseo_opengraph-title", " \n ", false},
			{"_yoast_wpseo_meta-robots-adv", "", false},
			{"_yoast_wpseo_title", "x", false}, {"_yoast_wpseo_focuskw", "x", false}, {"_yoast_wpseo_linkdex", "30", false},
			{"rank_math_facebook_title", "x", false}, {"_seopress_social_fb_title", "x", false},
		}},
		{"rankmath", []row{
			{"rank_math_facebook_title", "x", true}, {"rank_math_facebook_description", "x", true}, {"rank_math_twitter_title", "x", true},
			{"rank_math_twitter_description", "x", true}, {"rank_math_twitter_image", "https://example.com/x.png", true},
			{"rank_math_breadcrumb_title", "x", true},
			{"rank_math_robots", rankRobots("nofollow"), true}, {"rank_math_robots", rankRobots("noindex", "noarchive"), true},
			{"rank_math_robots", rankRobots("noimageindex"), true}, {"rank_math_robots", rankRobots("nosnippet", "index"), true},
			{"rank_math_robots", rankRobots("NoFollow"), true},
			{"rank_math_robots", rankRobots("noindex"), false}, {"rank_math_robots", rankRobots("index"), false}, {"rank_math_robots", rankRobots(), false},
			{"rank_math_robots", "nofollow", false}, {"rank_math_robots", `a:1:{i:0;s:8:"nofollow";`, false},
			{"rank_math_advanced_robots", `a:1:{s:11:"max-snippet";i:-1;}`, false}, {"rank_math_title", "x", false}, {"_yoast_wpseo_opengraph-title", "x", false},
		}},
		{"seopress", []row{
			{"_seopress_social_fb_title", "x", true}, {"_seopress_social_fb_desc", "x", true}, {"_seopress_social_twitter_title", "x", true},
			{"_seopress_social_twitter_desc", "x", true}, {"_seopress_social_twitter_img", "https://example.com/x.png", true},
			{"_seopress_robots_follow", "yes", true}, {"_seopress_robots_snippet", "yes", true}, {"_seopress_robots_imageindex", "yes", true},
			{"_seopress_robots_follow", "no", false}, {"_seopress_robots_snippet", "", false}, {"_seopress_robots_snippet", "no", false},
			{"_seopress_robots_imageindex", "1", false}, {"_seopress_robots_imageindex", "no", false},
			{"_seopress_robots_index", "yes", false}, {"_seopress_titles_title", "x", false},
		}},
		{"tsf", []row{
			{"_open_graph_title", "x", true}, {"_open_graph_description", "x", true}, {"_twitter_title", "x", true}, {"_twitter_description", "x", true},
			{"redirect", "https://example.com/x/", true}, {"redirect", "0", true},
			{"_genesis_nofollow", "1", true}, {"_genesis_noarchive", "1", true}, {"_tsf_title_no_blogname", "1", true},
			{"_genesis_nofollow", "0", false}, {"_genesis_noarchive", "-1", false}, {"_tsf_title_no_blogname", "0", false},
			{"_genesis_noindex", "1", false}, {"_genesis_title", "x", false},
		}},
		{"ssp", []row{
			{"ssp_meta_robots", "nofollow", true}, {"ssp_meta_robots", "noarchive", true}, {"ssp_meta_robots", "noindex, nofollow", true},
			{"ssp_meta_robots", "index,follow,NOARCHIVE", true},
			{"ssp_meta_robots", "noindex", false}, {"ssp_meta_robots", "index,follow", false}, {"ssp_meta_robots", "", false},
			{"ssp_meta_robots", "nofollowed", false}, {"ssp_meta_title", "x", false},
		}},
		{"aioseo", []row{
			{"_aioseo_og_title", "x", true}, {"_aioseo_og_description", "x", true}, {"_aioseo_twitter_title", "x", true},
			{"_aioseo_twitter_description", "x", true}, {"_aioseo_title", "x", false}, {"_aioseo_description", "x", false},
			{"_aioseo_canonical_url", "x", false},
		}},
		{"", []row{{"_yoast_wpseo_opengraph-title", "x", false}, {"redirect", "x", false}}},
	} {
		t.Run(tt.plugin, func(t *testing.T) {
			var items []wxrEntry
			counts := make(map[string]int)
			var order []string
			// A row that counts is two items, and one that does not is one, so that a condition that is turned round or left out
			// shows in the numbers.
			id := 100
			for i, r := range tt.rows {
				for range 1 + btoi(r.counted) {
					items = append(items, seoPost(id, fmt.Sprintf("Row %d", i), r.key, r.value))
					id++
					if r.counted {
						if counts[r.key] == 0 {
							order = append(order, r.key)
						}
						counts[r.key]++
					}
				}
			}
			imp, _, _ := seoRun(t, tt.plugin, items...)
			var want []string
			for _, k := range order {
				want = append(want, seoSkipLine(k, counts[k]))
			}
			sameSet(t, "the postmeta that are not imported", seoSkipLines(t, imp), want)
		})
	}

	t.Run("only the items that are made are counted", func(t *testing.T) {
		in := entriesWXR("", append(seoMedia(),
			seoPost(1, "One", "_yoast_wpseo_opengraph-title", "x"),
			wxrEntry{id: 2, status: "draft", title: "Draft", name: "draft", meta: metas("_yoast_wpseo_opengraph-title", "x")},
			wxrEntry{id: 3, status: "private", title: "Private", name: "private", meta: metas("_yoast_wpseo_opengraph-title", "x")},
			wxrEntry{id: 4, status: "trash", title: "Trash", name: "trash", meta: metas("_yoast_wpseo_opengraph-title", "x")},
			wxrEntry{id: 5, status: "auto-draft", title: "Auto", name: "auto", meta: metas("_yoast_wpseo_opengraph-title", "x")},
			wxrEntry{id: 6, title: "No date", name: "nodate", date: zeroDate, dateGMT: zeroDate, modified: zeroDate, modifiedGMT: zeroDate,
				meta: metas("_yoast_wpseo_opengraph-title", "x")},
			wxrEntry{id: 7, typ: "product", title: "Product", name: "product", meta: metas("_yoast_wpseo_opengraph-title", "x")},
			wxrEntry{id: 8, typ: "page", title: "Page", name: "page", meta: metas("_yoast_wpseo_opengraph-title", "x")},
		)...)
		imp, _, docs := makeEntries(t, seoSettings("yoast"), nil, in)
		// The posts 1 to 3 and the page 8.
		if len(docs) != 4 {
			t.Fatalf("%d entries, want 4", len(docs))
		}
		sameSet(t, "the postmeta that are not imported", seoSkipLines(t, imp), []string{seoSkipLine("_yoast_wpseo_opengraph-title", 4)})
	})
	t.Run("one record at the channel, however many postmeta", func(t *testing.T) {
		var items []wxrEntry
		for i := range 30 {
			items = append(items, seoPost(100+i, fmt.Sprintf("Post %d", i), "_yoast_wpseo_opengraph-title", "x", "_yoast_wpseo_opengraph-title", "y"))
		}
		imp, _, _ := seoRun(t, "yoast", items...)
		sameSet(t, "the postmeta that are not imported", seoSkipLines(t, imp), []string{seoSkipLine("_yoast_wpseo_opengraph-title", 30)})
	})
}

func TestEntriesSEOReportsOnce(t *testing.T) {
	imp, c, ei := newTidy(t, entriesWXR("", seoPost(1, "One", "_yoast_wpseo_opengraph-title", "x")))
	c.opts.Settings = seoSettings("yoast")
	c.setSEO(ei)
	c.reportSEO()
	// What was counted is not counted again.
	c.reportSEO()
	sameSet(t, "the postmeta that are not imported", seoSkipLines(t, imp), []string{seoSkipLine("_yoast_wpseo_opengraph-title", 1)})
}

func TestEntriesSEOIsTidied(t *testing.T) {
	bad := func(kind content.RefKind) string {
		t.Helper()
		err := content.CheckID(kind, "bad")
		if err == nil {
			t.Fatalf("%q is an ID of %s", "bad", kind)
		}
		return err.Error()
	}
	post := content.Revision{Title: "T", Slug: "t", Date: entryTime}
	with := func(edit func(*content.SEO)) content.Revision {
		r := post
		edit(&r.SEO)
		return r
	}
	long := strings.Repeat("a", content.MaxTextBytes+1)
	for _, tt := range []struct {
		name      string
		rev, want content.Revision
		lines     []string
	}{
		{"a title with a line break", with(func(s *content.SEO) { s.Title = "a\nb" }), post,
			[]string{"skipped: the title for search engines is not imported: contains a line break or tab -> {R}#/seo/title"}},
		{"a description with a line break", with(func(s *content.SEO) { s.Description = "a\tb" }), post,
			[]string{"skipped: the description for search engines is not imported: contains a line break or tab -> {R}#/seo/description"}},
		{"a title that is too long", with(func(s *content.SEO) { s.Title = long }), post,
			[]string{fmt.Sprintf("skipped: the title for search engines is not imported: too long: %d bytes (max %d) -> {R}#/seo/title", len(long), content.MaxTextBytes)}},
		{"a canonical URL that is not accepted", with(func(s *content.SEO) { s.Canonical = "javascript:x" }), post,
			[]string{`skipped: the canonical URL is not imported: scheme "javascript" is not allowed -> {R}#/seo/canonical`}},
		{"an image that is wrong", with(func(s *content.SEO) { s.OGImage = "bad" }), post,
			[]string{"skipped: the image for sharing is not imported: " + bad(content.RefMedia) + " -> {R}#/seo/ogImage"}},
		{"only the one that is wrong is taken out", with(func(s *content.SEO) { s.Title = "T"; s.Canonical = "x"; s.NoIndex = true; s.Description = "D" }),
			with(func(s *content.SEO) { s.Title = "T"; s.NoIndex = true; s.Description = "D" }),
			[]string{`skipped: the canonical URL is not imported: must be an absolute URL or a path starting with "/" -> {R}#/seo/canonical`}},
		{"two that are wrong", with(func(s *content.SEO) { s.Title = "a\nb"; s.OGImage = "bad"; s.Canonical = "/x/" }), with(func(s *content.SEO) { s.Canonical = "/x/" }),
			[]string{
				"skipped: the title for search engines is not imported: contains a line break or tab -> {R}#/seo/title",
				"skipped: the image for sharing is not imported: " + bad(content.RefMedia) + " -> {R}#/seo/ogImage",
			}},
		{"values that are right", with(func(s *content.SEO) {
			s.Title = "A"
			s.Description = "B"
			s.Canonical = "https://example.org/"
			s.NoIndex = true
		}),
			with(func(s *content.SEO) {
				s.Title = "A"
				s.Description = "B"
				s.Canonical = "https://example.org/"
				s.NoIndex = true
			}), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, c, ei := newTidy(t, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"}))
			ei.status = content.StatusDraft
			ei.rev = tt.rev
			c.tidy(ei)
			var want []string
			for _, l := range tt.lines {
				want = append(want, strings.ReplaceAll(l, "{R}", "revisions/"+ei.revID+".json"))
			}
			sameSet(t, "the report", itemLines(imp.Report, 5), want)
			if !reflect.DeepEqual(ei.rev, tt.want) {
				t.Errorf("the revision is %+v, want %+v", ei.rev, tt.want)
			}
		})
	}

	// What is wrong with the SEO settings costs the entry nothing else, the body least of all: the guard of emitAll, which
	// takes the body back, never has the SEO settings to blame.
	t.Run("the body stays", func(t *testing.T) {
		imp, _, docs := seoRun(t, "yoast", wxrEntry{
			id: 1, title: "About", name: "about", content: "<p>Hello</p>",
			meta: metas("_yoast_wpseo_canonical", "javascript:x", "_yoast_wpseo_title", "Custom"),
		})
		d := docOf(docs, 1)
		if len(d.Revision.Body) == 0 || d.Revision.SEO != (content.SEO{Title: "Custom"}) {
			t.Errorf("the revision is %+v", d.Revision)
		}
		wantItem(t, imp, docs, 1, `skipped: the canonical URL is not imported: scheme "javascript" is not allowed -> {R}#/seo/canonical`)
	})
}

func TestUnitName(t *testing.T) {
	for unit, want := range map[string]string{
		"/title": "title", "/featuredImage": "featuredImage", "/terms/category": "terms/category", "": "",
		"/seo/title": "title for search engines", "/seo/description": "description for search engines",
		"/seo/canonical": "canonical URL", "/seo/ogImage": "image for sharing", "/seo/noindex": "seo/noindex",
		"/fields/client": `custom field "client"`, "/fields/a~1b": `custom field "a/b"`, "/fields/": `custom field ""`,
	} {
		if got := unitName(unit); got != want {
			t.Errorf("unitName(%q) = %q, want %q", unit, got, want)
		}
	}
}

func TestEntriesYoastPrimaryCategory(t *testing.T) {
	const rule = "/{category}/{slug}/"
	terms := []wxrTerm{
		{id: 1, taxonomy: "category", slug: "uncategorized", name: "Uncategorized"},
		{id: 2, taxonomy: "category", slug: "news", name: "News"},
		{id: 4, taxonomy: "category", slug: "tech", name: "Tech"},
		{id: 6, taxonomy: "category", slug: "life", name: "Life"},
		{id: 7, taxonomy: "category", slug: "travel", name: "Travel"},
		{id: 9, taxonomy: "category", slug: "other", name: "Other"},
	}
	cats := func(slugs ...string) []wxrTermRef { return termRefs("category", slugs...) }
	const primary = "_yoast_wpseo_primary_category"
	item := func(id int, link string, primaryValue string, refs ...wxrTermRef) wxrEntry {
		x := wxrEntry{id: id, title: fmt.Sprintf("Post %d", id), name: fmt.Sprintf("post-%d", id), terms: refs}
		if link != "" {
			x.link = "https://example.com/" + link + fmt.Sprintf("/post-%d/", id)
		}
		if primaryValue != "" {
			x.meta = metas(primary, primaryValue)
		}
		return x
	}
	items := []wxrEntry{
		// The link has the category with the lowest ID: the primary category is another of the item, and goes first.
		item(1, "news", "4", cats("news", "tech", "life")...),
		item(2, "news", " 6\n", cats("tech", "life", "news")...),
		// The primary category is the one with the lowest ID.
		item(3, "news", "2", cats("tech", "news")...),
		// The link has another category: it is what WordPress used.
		item(4, "tech", "2", cats("news", "tech")...),
		item(5, "life", "2", cats("news", "tech", "life")...),
		// A link that no category of the item makes, and an item that has none.
		item(6, "other", "6", cats("news", "tech", "life")...),
		item(7, "", "6", cats("news", "tech", "life")...),
		// The primary category is not one of the item, or is not a category ID.
		item(8, "news", "9", cats("news", "tech")...),
		item(9, "news", "99", cats("news", "tech")...),
		item(10, "news", "abc", cats("news", "tech")...),
		item(11, "news", "0", cats("news", "tech")...),
		item(12, "news", "-4", cats("news", "tech")...),
		item(13, "news", "4.0", cats("news", "tech")...),
		item(14, "news", "+4", cats("news", "tech")...),
		item(15, "news", "", cats("news", "tech")...),
		item(16, "news", "04", cats("news", "tech")...),
		// An item with one category, and an item with none.
		item(17, "news", "4", cats("news")...),
		item(18, "uncategorized", "4"),
		// The others keep their order.
		item(19, "news", "7", cats("tech", "news", "travel", "life")...),
	}
	for _, tt := range []struct {
		plugin string
		want   map[int64][]string
	}{
		{"yoast", map[int64][]string{
			1: {"tech", "news", "life"}, 2: {"life", "tech", "news"}, 3: {"news", "tech"}, 4: {"tech", "news"}, 5: {"life", "news", "tech"},
			6: {"life", "news", "tech"}, 7: {"life", "news", "tech"}, 8: {"news", "tech"}, 9: {"news", "tech"}, 10: {"news", "tech"},
			11: {"news", "tech"}, 12: {"news", "tech"}, 13: {"news", "tech"}, 14: {"news", "tech"}, 15: {"news", "tech"},
			16: {"tech", "news"}, 17: {"news"}, 18: {"uncategorized"}, 19: {"travel", "tech", "news", "life"},
		}},
		{"", map[int64][]string{1: {"news", "tech", "life"}, 2: {"news", "tech", "life"}, 6: {"news", "tech", "life"}, 16: {"news", "tech"}, 19: {"news", "tech", "travel", "life"}}},
		{"rankmath", map[int64][]string{1: {"news", "tech", "life"}, 2: {"news", "tech", "life"}, 6: {"news", "tech", "life"}, 16: {"news", "tech"}, 19: {"news", "tech", "travel", "life"}}},
		{"aioseo", map[int64][]string{1: {"news", "tech", "life"}, 7: {"news", "tech", "life"}}},
	} {
		t.Run(tt.plugin, func(t *testing.T) {
			s := refsSettings(rule)
			s.DefaultCategory = "uncategorized"
			s.SEO.Plugin = tt.plugin
			imp, _, docs := makeRefs(t, s, nil, refsChannel(nil, terms), items...)
			for post, want := range tt.want {
				if got, w := docOf(docs, post).Revision.Terms["category"], termIDs(t, imp, "category", want...); !slices.Equal(got, w) {
					t.Errorf("the categories of item %d are %v, want %v (%v)", post, got, w, want)
				}
			}
			// The primary category is not recorded, and everything of an item that is the other in it is the same.
			for post := int64(1); post <= 19; post++ {
				if post == 18 {
					continue
				}
				wantItem(t, imp, docs, post)
			}
		})
	}

	t.Run("a category without an ID", func(t *testing.T) {
		old := []wxrTerm{{taxonomy: "category", slug: "a", name: "A"}, {id: 5, taxonomy: "category", slug: "b", name: "B"}}
		s := refsSettings(rule)
		s.SEO.Plugin = "yoast"
		imp, _, docs := makeRefs(t, s, nil, refsChannel(nil, old),
			item(1, "other", "5", cats("a", "b")...), item(2, "other", "0", cats("a", "b")...))
		for post, want := range map[int64][]string{1: {"b", "a"}, 2: {"b", "a"}} {
			if got, w := docOf(docs, post).Revision.Terms["category"], termIDs(t, imp, "category", want...); !slices.Equal(got, w) {
				t.Errorf("the categories of item %d are %v, want %v (%v)", post, got, w, want)
			}
		}
	})
	t.Run("a custom type that has no category in its URL rule", func(t *testing.T) {
		s := refsSettings(rule)
		s.SEO.Plugin = "yoast"
		defs := &content.Site{
			Types:      []content.TypeDef{{Name: "post", Taxonomies: []string{"genre"}}, {Name: "works", Label: "Works", Body: true, Taxonomies: []string{"category"}}},
			Taxonomies: []content.TaxonomyDef{{Name: "genre", Hierarchical: true}},
		}
		w := item(1, "", "4", cats("news", "tech")...)
		w.typ = "works"
		imp, _, docs := makeRefs(t, s, defs, refsChannel(nil, terms), w)
		if got, want := docOf(docs, 1).Revision.Terms["category"], termIDs(t, imp, "category", "news", "tech"); !slices.Equal(got, want) {
			t.Errorf("the categories are %v, want %v", got, want)
		}
	})
}
