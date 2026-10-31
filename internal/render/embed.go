package render

import (
	"html/template"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// embedFrames are the iframes of the providers without their src, as the embed codes of the providers write them. x has
// no iframe: its posts are rendered as links.
var embedFrames = map[string]Embed{
	"youtube": {
		Title: "YouTube", Width: 560, Height: 315,
		Allow: "accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share",
		// YouTube refuses to play without a referrer.
		ReferrerPolicy: "strict-origin-when-cross-origin",
	},
	"vimeo": {
		Title: "Vimeo", Width: 640, Height: 360,
		Allow:          "autoplay; fullscreen; picture-in-picture; clipboard-write; encrypted-media; web-share",
		ReferrerPolicy: "strict-origin-when-cross-origin",
	},
	"x":          {Title: "X"},
	"googleMaps": {Title: "Google Maps", Width: 600, Height: 450, ReferrerPolicy: "no-referrer-when-downgrade"},
}

// embed returns the data of b, an embed at path; ok is false if its provider is unknown or its URL cannot be embedded,
// which it warns about before anything else.
func (w *walker) embed(b *content.Block, path string) (Embed, bool) {
	a := &b.Attrs
	d, ok := embedFrames[a.Provider]
	if !ok {
		w.warnf(path+"/attrs/provider", "unknown provider %q; not rendered", a.Provider)
		return Embed{}, false
	}
	// ParseEmbedURL leaves credentials, backslashes and control characters to CheckHref.
	err := content.CheckHref(a.URL)
	var t content.EmbedTarget
	if err == nil {
		t, err = content.ParseEmbedURL(a.Provider, a.URL)
	}
	if err != nil {
		w.warnf(path+"/attrs/url", "invalid embed URL %q: %v; not rendered", a.URL, err)
		return Embed{}, false
	}
	d.Common = w.common(b, path)
	d.Provider = a.Provider
	d.URL = template.URL(encodeURL(a.URL))
	switch a.Provider {
	case "youtube":
		// Queries such as the start time t are dropped.
		d.Src = template.URL(encodeURL("https://www.youtube-nocookie.com/embed/" + t.ID))
	case "vimeo":
		query := "?dnt=1"
		if t.Hash != "" {
			query = "?h=" + t.Hash + "&dnt=1"
		}
		d.Src = template.URL(encodeURL("https://player.vimeo.com/video/" + t.ID + query))
	case "googleMaps":
		d.Src = d.URL
	}
	d.Caption = template.HTML(w.richText(a.Caption, path+"/attrs/caption"))
	if s := strings.TrimSpace(plainText(a.Caption)); s != "" {
		d.Title = s
	}
	return d, true
}
