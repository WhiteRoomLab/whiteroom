package content

import "testing"

// Messages of checkEmbedURL.
const (
	embedHTTPS = "must be an https URL"
	embedPort  = "ports are not allowed"
	notYouTube = "not a URL that youtube can embed"
	notVimeo   = "not a URL that vimeo can embed"
	notX       = "not a URL that x can embed"
	notMaps    = "not a URL that googleMaps can embed"
)

const mapsEmbed = "https://www.google.com/maps/embed?pb=!1m18!1m12!1m3!1d3240.828!2d139.764!3d35.681!2m3!1f0!2f0!3f0" +
	"!3m2!1i1024!2i768!4f13.1!3m3!1m2!1s0x60188bfbd89f700b%3A0x277c49ba34ed38!2z5p2x5Lqs6aeF!5e0!3m2!1sja!2sjp!4v1700000000000"

var checkEmbedURLTests = []struct {
	name     string
	provider string
	url      string
	href     string // message of checkHref
	msg      string
}{
	{name: "youtube watch", provider: "youtube", url: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
	{name: "youtube watch with t and si", provider: "youtube", url: "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s&si=B_RZg_I-lLaa7UU-"},
	{name: "youtube watch in a playlist", provider: "youtube", url: "https://www.youtube.com/watch?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf&v=dQw4w9WgXcQ&index=2"},
	{name: "youtube watch without www", provider: "youtube", url: "https://youtube.com/watch?v=dQw4w9WgXcQ"},
	{name: "youtube mobile", provider: "youtube", url: "https://m.youtube.com/watch?v=dQw4w9WgXcQ"},
	{name: "youtu.be", provider: "youtube", url: "https://youtu.be/dQw4w9WgXcQ"},
	{name: "youtu.be with si", provider: "youtube", url: "https://youtu.be/dQw4w9WgXcQ?si=B_RZg_I-lLaa7UU-"},
	{name: "youtu.be with trailing slash", provider: "youtube", url: "https://youtu.be/dQw4w9WgXcQ/"},
	{name: "youtu.be in upper case", provider: "youtube", url: "HTTPS://YOUTU.BE/dQw4w9WgXcQ"},
	{name: "youtube ID with hyphens and underscores", provider: "youtube", url: "https://youtu.be/a-b_c-d_e-f"},
	{name: "youtube shorts", provider: "youtube", url: "https://youtube.com/shorts/aqz-KE-bpKQ?si=B_RZg_I-lLaa7UU-"},
	{name: "youtube live", provider: "youtube", url: "https://www.youtube.com/live/jfKfPfyJRdk?si=B_RZg_I-lLaa7UU-"},
	{name: "youtube embed", provider: "youtube", url: "https://www.youtube.com/embed/dQw4w9WgXcQ?si=B_RZg_I-lLaa7UU-"},
	{name: "youtube-nocookie embed", provider: "youtube", url: "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"},

	{name: "youtube ID of 10 characters", provider: "youtube", url: "https://youtu.be/dQw4w9WgXc", msg: notYouTube},
	{name: "youtube ID of 12 characters", provider: "youtube", url: "https://www.youtube.com/watch?v=dQw4w9WgXcQx", msg: notYouTube},
	{name: "youtube ID with a dot", provider: "youtube", url: "https://www.youtube.com/embed/dQw4w9WgXc.", msg: notYouTube},
	{name: "youtube watch without v", provider: "youtube", url: "https://www.youtube.com/watch?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf", msg: notYouTube},
	{name: "youtube v on another path", provider: "youtube", url: "https://www.youtube.com/watch_popup?v=dQw4w9WgXcQ", msg: notYouTube},
	{name: "youtube playlist", provider: "youtube", url: "https://www.youtube.com/playlist?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf", msg: notYouTube},
	{name: "youtube playlist embed", provider: "youtube", url: "https://www.youtube.com/embed/videoseries?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf", msg: notYouTube},
	{name: "youtube channel", provider: "youtube", url: "https://www.youtube.com/channel/UCuAXFkgsw1L7xaCfnd5JJOw", msg: notYouTube},
	{name: "youtube embed with an extra segment", provider: "youtube", url: "https://www.youtube.com/embed/dQw4w9WgXcQ/x", msg: notYouTube},
	{name: "youtu.be without ID", provider: "youtube", url: "https://youtu.be/", msg: notYouTube},
	{name: "youtu.be with an extra segment", provider: "youtube", url: "https://youtu.be/dQw4w9WgXcQ/x", msg: notYouTube},
	{name: "youtube on another subdomain", provider: "youtube", url: "https://music.youtube.com/watch?v=dQw4w9WgXcQ", msg: notYouTube},
	{name: "youtube.com as a subdomain", provider: "youtube", url: "https://www.youtube.com.example.com/watch?v=dQw4w9WgXcQ", msg: notYouTube},
	{name: "http", provider: "youtube", url: "http://www.youtube.com/watch?v=dQw4w9WgXcQ", msg: embedHTTPS},
	{name: "path without host", provider: "youtube", url: "/watch?v=dQw4w9WgXcQ", msg: embedHTTPS},
	{name: "port", provider: "youtube", url: "https://www.youtube.com:8443/watch?v=dQw4w9WgXcQ", msg: embedPort},
	{name: "default port", provider: "youtube", url: "https://youtu.be:443/dQw4w9WgXcQ", msg: embedPort},

	{name: "vimeo", provider: "vimeo", url: "https://vimeo.com/76979871"},
	{name: "vimeo with share", provider: "vimeo", url: "https://vimeo.com/76979871?share=copy"},
	{name: "vimeo with www", provider: "vimeo", url: "https://www.vimeo.com/76979871"},
	{name: "vimeo unlisted", provider: "vimeo", url: "https://vimeo.com/76979871/8272103f6e"},
	{name: "vimeo player", provider: "vimeo", url: "https://player.vimeo.com/video/76979871?h=8272103f6e"},

	{name: "vimeo in a channel", provider: "vimeo", url: "https://vimeo.com/channels/staffpicks/76979871", msg: notVimeo},
	{name: "vimeo user", provider: "vimeo", url: "https://vimeo.com/staff", msg: notVimeo},
	{name: "vimeo with a segment that is not a hash", provider: "vimeo", url: "https://vimeo.com/76979871/likes", msg: notVimeo},
	{name: "vimeo player without video", provider: "vimeo", url: "https://player.vimeo.com/76979871", msg: notVimeo},
	{
		name:     "non-ASCII letter that strings.ToLower maps to i",
		provider: "vimeo",
		url:      "https://v" + string(rune(0x0130)) + "meo.com/76979871",
		msg:      notVimeo,
	},
	{name: "youtube URL for vimeo", provider: "vimeo", url: "https://youtu.be/dQw4w9WgXcQ", msg: notVimeo},

	{name: "x", provider: "x", url: "https://x.com/jack/status/20"},
	{name: "x with query", provider: "x", url: "https://x.com/jack/status/20?s=20"},
	{name: "x with www", provider: "x", url: "https://www.x.com/jack/status/20"},
	{name: "x mobile", provider: "x", url: "https://mobile.x.com/jack/status/20"},
	{name: "twitter", provider: "x", url: "https://twitter.com/jack/status/20?ref_src=twsrc%5Etfw"},
	{name: "twitter with www", provider: "x", url: "https://www.twitter.com/jack/status/20"},
	{name: "twitter mobile", provider: "x", url: "https://mobile.twitter.com/jack/status/20"},
	{name: "x user of 15 characters", provider: "x", url: "https://x.com/abcdefghij_1234/status/1234567890123456789"},

	{name: "x profile", provider: "x", url: "https://x.com/jack", msg: notX},
	{name: "x status in letters", provider: "x", url: "https://x.com/jack/status/abc", msg: notX},
	{name: "x list", provider: "x", url: "https://x.com/i/lists/1234567890", msg: notX},
	{name: "x user of 16 characters", provider: "x", url: "https://x.com/abcdefghij_12345/status/20", msg: notX},

	{name: "google maps embed", provider: "googleMaps", url: mapsEmbed},

	{name: "google maps place", provider: "googleMaps", url: "https://www.google.com/maps/place/Tokyo+Station/@35.6812362,139.7671248,17z", msg: notMaps},
	{name: "google maps embed without pb", provider: "googleMaps", url: "https://www.google.com/maps/embed?q=Tokyo", msg: notMaps},
	{name: "google maps embed with empty pb", provider: "googleMaps", url: "https://www.google.com/maps/embed?pb=", msg: notMaps},
	{name: "google maps pb on another path", provider: "googleMaps", url: "https://www.google.com/maps?pb=!1m18", msg: notMaps},
	{name: "google maps short link", provider: "googleMaps", url: "https://maps.app.goo.gl/u5mJ3EZ9RaFHA3Mx6", msg: notMaps},
	{name: "maps.google.com", provider: "googleMaps", url: "https://maps.google.com/maps/embed?pb=!1m18", msg: notMaps},
	{name: "google.co.jp", provider: "googleMaps", url: "https://www.google.co.jp/maps/embed?pb=!1m18", msg: notMaps},

	{name: "unknown provider", provider: "instagram", url: "https://www.instagram.com/p/C0abcDEFghi/", msg: `unknown provider "instagram"`},
	{name: "provider in another case", provider: "YouTube", url: "https://youtu.be/dQw4w9WgXcQ", msg: `unknown provider "YouTube"`},
	{name: "invalid URL", provider: "youtube", url: "https://youtu.be/%zz", href: "invalid URL", msg: "invalid URL"},
}

func TestCheckEmbedURL(t *testing.T) {
	for _, tt := range checkEmbedURLTests {
		t.Run(tt.name, func(t *testing.T) {
			// checkEmbedURL takes URLs that passed checkHref.
			assertErrorMsg(t, checkHref(tt.url), tt.href)
			assertErrorMsg(t, checkEmbedURL(tt.provider, tt.url), tt.msg)
		})
	}
}

func TestParseEmbedURL(t *testing.T) {
	const (
		video = "dQw4w9WgXcQ"
		hash  = "8272103f6e"
	)
	tests := []struct {
		name     string
		provider string
		url      string
		want     EmbedTarget
	}{
		{name: "youtube watch", provider: "youtube", url: "https://www.youtube.com/watch?v=" + video, want: EmbedTarget{ID: video}},
		{
			name:     "youtube watch with t and si",
			provider: "youtube",
			url:      "https://www.youtube.com/watch?v=" + video + "&t=42s&si=B_RZg_I-lLaa7UU-",
			want:     EmbedTarget{ID: video},
		},
		{
			name:     "youtube watch in a playlist",
			provider: "youtube",
			url:      "https://www.youtube.com/watch?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf&v=" + video + "&index=2",
			want:     EmbedTarget{ID: video},
		},
		{name: "youtu.be", provider: "youtube", url: "https://youtu.be/" + video + "?si=B_RZg_I-lLaa7UU-", want: EmbedTarget{ID: video}},
		{name: "youtube shorts", provider: "youtube", url: "https://youtube.com/shorts/aqz-KE-bpKQ", want: EmbedTarget{ID: "aqz-KE-bpKQ"}},
		{name: "youtube live", provider: "youtube", url: "https://www.youtube.com/live/jfKfPfyJRdk", want: EmbedTarget{ID: "jfKfPfyJRdk"}},
		{name: "youtube embed", provider: "youtube", url: "https://www.youtube.com/embed/" + video, want: EmbedTarget{ID: video}},
		{name: "youtube-nocookie embed", provider: "youtube", url: "https://www.youtube-nocookie.com/embed/" + video, want: EmbedTarget{ID: video}},
		{name: "youtube host in upper case", provider: "youtube", url: "HTTPS://YOUTU.BE/" + video, want: EmbedTarget{ID: video}},

		{name: "vimeo", provider: "vimeo", url: "https://vimeo.com/76979871", want: EmbedTarget{ID: "76979871"}},
		{name: "vimeo with www", provider: "vimeo", url: "https://www.vimeo.com/76979871?share=copy", want: EmbedTarget{ID: "76979871"}},
		{name: "vimeo unlisted", provider: "vimeo", url: "https://vimeo.com/76979871/" + hash, want: EmbedTarget{ID: "76979871", Hash: hash}},
		{
			// The hash in the path wins.
			name:     "vimeo unlisted with another h",
			provider: "vimeo",
			url:      "https://vimeo.com/76979871/" + hash + "?h=0123456789",
			want:     EmbedTarget{ID: "76979871", Hash: hash},
		},
		{
			// Only the player reads h.
			name:     "vimeo with h",
			provider: "vimeo",
			url:      "https://vimeo.com/76979871?h=" + hash,
			want:     EmbedTarget{ID: "76979871"},
		},
		{
			name:     "vimeo player with h",
			provider: "vimeo",
			url:      "https://player.vimeo.com/video/76979871?h=" + hash,
			want:     EmbedTarget{ID: "76979871", Hash: hash},
		},
		{name: "vimeo player without h", provider: "vimeo", url: "https://player.vimeo.com/video/76979871", want: EmbedTarget{ID: "76979871"}},
		{
			name:     "vimeo player with h in upper case",
			provider: "vimeo",
			url:      "https://player.vimeo.com/video/76979871?h=8272103F6E",
			want:     EmbedTarget{ID: "76979871"},
		},
		{
			name:     "vimeo player with h that is not hex",
			provider: "vimeo",
			url:      "https://player.vimeo.com/video/76979871?h=xyz",
			want:     EmbedTarget{ID: "76979871"},
		},

		{name: "x", provider: "x", url: "https://x.com/jack/status/20", want: EmbedTarget{ID: "20", User: "jack"}},
		{name: "twitter", provider: "x", url: "https://twitter.com/jack/status/20?ref_src=twsrc%5Etfw", want: EmbedTarget{ID: "20", User: "jack"}},
		{name: "x mobile with query", provider: "x", url: "https://mobile.x.com/jack/status/20?s=46", want: EmbedTarget{ID: "20", User: "jack"}},

		// The renderer embeds the URL itself.
		{name: "google maps embed", provider: "googleMaps", url: mapsEmbed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEmbedURL(tt.provider, tt.url)
			if err != nil || got != tt.want {
				t.Errorf("ParseEmbedURL(%q, %q) = %#v, %v; want %#v, nil", tt.provider, tt.url, got, err, tt.want)
			}
		})
	}
	// The URLs that checkEmbedURL rejects give its messages and the zero target.
	for _, tt := range checkEmbedURLTests {
		if tt.msg == "" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEmbedURL(tt.provider, tt.url)
			assertErrorMsg(t, err, tt.msg)
			if got != (EmbedTarget{}) {
				t.Errorf("ParseEmbedURL(%q, %q) = %#v, want the zero target", tt.provider, tt.url, got)
			}
		})
	}
}
