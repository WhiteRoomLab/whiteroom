package content

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	youTubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	digitsPattern    = regexp.MustCompile(`^[0-9]+$`)
	lowerHexPattern  = regexp.MustCompile(`^[0-9a-f]+$`)
	xUserPattern     = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
)

// EmbedTarget is what an embed URL refers to.
type EmbedTarget struct {
	ID   string // youtube: the video ID; vimeo: the video number; x: the number of the post
	Hash string // vimeo: the hash of an unlisted video, from the path or from the query h; "" if none
	User string // x: the user name
}

var embedProviders = map[string]func(host string, path []string, query url.Values) (EmbedTarget, bool){
	"youtube":    parseYouTubeURL,
	"vimeo":      parseVimeoURL,
	"x":          parseXURL,
	"googleMaps": parseGoogleMapsURL,
}

// ParseEmbedURL returns what s, an embed URL of provider that passed CheckHref, refers to; the error is that of
// validation.
func ParseEmbedURL(provider, s string) (EmbedTarget, error) {
	parse, ok := embedProviders[provider]
	if !ok {
		return EmbedTarget{}, fmt.Errorf("unknown provider %q", provider)
	}
	u, err := url.Parse(s)
	if err != nil {
		return EmbedTarget{}, errors.New("invalid URL")
	}
	if u.Scheme != "https" {
		return EmbedTarget{}, errors.New("must be an https URL")
	}
	if u.Port() != "" {
		return EmbedTarget{}, errors.New("ports are not allowed")
	}
	// Not strings.ToLower: it maps U+0130 to "i", but browsers map it to "i" and U+0307, another domain.
	host := strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, u.Hostname())
	path := strings.Split(strings.Trim(u.Path, "/"), "/")
	t, ok := parse(host, path, u.Query())
	if !ok {
		return EmbedTarget{}, fmt.Errorf("not a URL that %s can embed", provider)
	}
	return t, nil
}

// checkEmbedURL checks that provider can embed s, a URL that passed checkHref.
func checkEmbedURL(provider, s string) error {
	_, err := ParseEmbedURL(provider, s)
	return err
}

func parseYouTubeURL(host string, path []string, query url.Values) (EmbedTarget, bool) {
	id := ""
	switch host {
	case "youtu.be":
		if len(path) == 1 {
			id = path[0]
		}
	case "youtube.com", "www.youtube.com", "m.youtube.com", "www.youtube-nocookie.com":
		switch {
		case len(path) == 1 && path[0] == "watch":
			id = query.Get("v")
		case len(path) == 2 && slices.Contains([]string{"embed", "shorts", "live"}, path[0]):
			// "/embed/videoseries?list=" embeds a playlist, not a video with that ID.
			if path[1] != "videoseries" {
				id = path[1]
			}
		}
	}
	if !youTubeIDPattern.MatchString(id) {
		return EmbedTarget{}, false
	}
	return EmbedTarget{ID: id}, true
}

func parseVimeoURL(host string, path []string, query url.Values) (EmbedTarget, bool) {
	switch host {
	case "vimeo.com", "www.vimeo.com":
		// An unlisted video has a hash after its ID.
		if digitsPattern.MatchString(path[0]) && (len(path) == 1 || len(path) == 2 && lowerHexPattern.MatchString(path[1])) {
			t := EmbedTarget{ID: path[0]}
			if len(path) == 2 {
				t.Hash = path[1]
			}
			return t, true
		}
	case "player.vimeo.com":
		if len(path) == 2 && path[0] == "video" && digitsPattern.MatchString(path[1]) {
			t := EmbedTarget{ID: path[1]}
			// Not an error: validation ignores queries other than v and pb.
			if h := query.Get("h"); lowerHexPattern.MatchString(h) {
				t.Hash = h
			}
			return t, true
		}
	}
	return EmbedTarget{}, false
}

func parseXURL(host string, path []string, _ url.Values) (EmbedTarget, bool) {
	switch host {
	case "x.com", "www.x.com", "mobile.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com":
		if len(path) == 3 && xUserPattern.MatchString(path[0]) && path[1] == "status" && digitsPattern.MatchString(path[2]) {
			return EmbedTarget{ID: path[2], User: path[0]}, true
		}
	}
	return EmbedTarget{}, false
}

// parseGoogleMapsURL gives the zero target: the URL itself is the src of the iframe.
func parseGoogleMapsURL(host string, path []string, query url.Values) (EmbedTarget, bool) {
	return EmbedTarget{}, host == "www.google.com" && slices.Equal(path, []string{"maps", "embed"}) && query.Get("pb") != ""
}
