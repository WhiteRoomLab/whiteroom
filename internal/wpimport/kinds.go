package wpimport

import "github.com/WhiteRoomLab/whiteroom/internal/content"

// Taxonomies that other conversions handle: the conversion of the terms leaves them alone and says nothing.
var ownedTaxonomies = map[string]bool{
	"nav_menu": true, // the menus
	// Polylang keeps the languages and the translations in taxonomies.
	"language": true, "term_language": true, "post_translations": true, "term_translations": true,
}

// Taxonomies that nothing in whiteroom corresponds to: their terms are not imported, as the report says.
var unusedTaxonomies = map[string]bool{
	"post_format": true, "link_category": true, "wp_theme": true, "wp_template_part_area": true,
	"wp_pattern_category": true,
}

// Post types whose items are not entries, or that other conversions handle.
var ownedTypes = map[string]bool{
	"attachment": true, "revision": true, "nav_menu_item": true, "wpcf7_contact_form": true, "custom_css": true,
	"customize_changeset": true, "oembed_cache": true, "user_request": true, "wp_block": true, "wp_template": true,
	"wp_template_part": true, "wp_global_styles": true, "wp_navigation": true, "wp_font_family": true,
	"wp_font_face": true,
}

// taxonomyName returns the name in the site settings of the taxonomy wp of WordPress, or "" if its terms are not
// imported: category and post_tag are category and tag, and a custom taxonomy is the one that the site defines with the
// same name. A taxonomy named tag is not imported, as it would be taken for the tag. A nil site defines nothing.
func taxonomyName(wp string, site *content.Site) string {
	switch wp {
	case "category":
		return content.TaxonomyCategory
	case "post_tag":
		return content.TaxonomyTag
	case "tag":
		return ""
	}
	if ownedTaxonomies[wp] || unusedTaxonomies[wp] || site == nil || site.Taxonomy(wp) == nil {
		return ""
	}
	return wp
}

// typeName returns the name in the site settings of the post type wp of WordPress, or "" if its items are not imported
// as entries: post and page are post and page, and a custom post type is the one that the site defines with the same
// name. A nil site defines nothing.
func typeName(wp string, site *content.Site) string {
	switch wp {
	case "post":
		return content.TypePost
	case "page":
		return content.TypePage
	}
	if ownedTypes[wp] || site == nil || site.Type(wp) == nil {
		return ""
	}
	return wp
}
