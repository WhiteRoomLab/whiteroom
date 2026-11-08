package wpimport

import (
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func kindsSite() *content.Site {
	return &content.Site{
		Languages:       []string{"ja"},
		DefaultLanguage: "ja",
		Taxonomies: []content.TaxonomyDef{
			{Name: "genre", Hierarchical: true},
			{Name: "category", Label: "Topics"},
			{Name: "post_format"},
			{Name: "nav_menu"},
			{Name: "series"},
		},
		Types: []content.TypeDef{
			{Name: "works", Body: true},
			{Name: "page", Label: "Pages"},
			{Name: "attachment"},
			{Name: "wp_block"},
			{Name: "event"},
		},
	}
}

func TestTaxonomyName(t *testing.T) {
	site := kindsSite()
	for _, tt := range []struct {
		wp   string
		site *content.Site
		want string
	}{
		{"category", site, "category"},
		{"post_tag", site, "tag"},
		{"category", nil, "category"},
		{"post_tag", nil, "tag"},
		{"category", &content.Site{}, "category"},
		{"genre", site, "genre"},
		{"series", site, "series"},
		{"genre", nil, ""},
		{"genre", &content.Site{}, ""},
		{"unknown", site, ""},
		{"unknown", nil, ""},
		{"tag", site, ""},
		{"tag", nil, ""},
		{"", site, ""},
		// What other conversions handle, and what whiteroom has no use for, are not terms, also if the site defines them.
		{"nav_menu", site, ""},
		{"post_format", site, ""},
		{"language", site, ""},
		{"term_language", site, ""},
		{"post_translations", site, ""},
		{"term_translations", site, ""},
		{"link_category", site, ""},
		{"wp_theme", site, ""},
		{"wp_template_part_area", site, ""},
		{"wp_pattern_category", site, ""},
	} {
		if got := taxonomyName(tt.wp, tt.site); got != tt.want {
			t.Errorf("taxonomyName(%q, site %v) = %q, want %q", tt.wp, tt.site != nil, got, tt.want)
		}
	}
	for wp := range ownedTaxonomies {
		if unusedTaxonomies[wp] {
			t.Errorf("%q is in ownedTaxonomies and unusedTaxonomies", wp)
		}
	}
}

func TestTypeName(t *testing.T) {
	site := kindsSite()
	for _, tt := range []struct {
		wp   string
		site *content.Site
		want string
	}{
		{"post", site, "post"},
		{"page", site, "page"},
		{"post", nil, "post"},
		{"page", nil, "page"},
		{"post", &content.Site{}, "post"},
		{"works", site, "works"},
		{"event", site, "event"},
		{"works", nil, ""},
		{"works", &content.Site{}, ""},
		{"product", site, ""},
		{"product", nil, ""},
		{"", site, ""},
		// What is not an entry, or is handled by another conversion, is not mapped, also if the site defines it.
		{"attachment", site, ""},
		{"wp_block", site, ""},
		{"revision", site, ""},
		{"nav_menu_item", site, ""},
		{"wpcf7_contact_form", site, ""},
		{"custom_css", site, ""},
		{"customize_changeset", site, ""},
		{"oembed_cache", site, ""},
		{"user_request", site, ""},
		{"wp_template", site, ""},
		{"wp_template_part", site, ""},
		{"wp_global_styles", site, ""},
		{"wp_navigation", site, ""},
		{"wp_font_family", site, ""},
		{"wp_font_face", site, ""},
	} {
		if got := typeName(tt.wp, tt.site); got != tt.want {
			t.Errorf("typeName(%q, site %v) = %q, want %q", tt.wp, tt.site != nil, got, tt.want)
		}
	}
}
