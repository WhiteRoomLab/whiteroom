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

var embedProviders = map[string]func(host string, path []string, query url.Values) bool{
	"youtube":    isYouTubeURL,
	"vimeo":      isVimeoURL,
	"x":          isXURL,
	"googleMaps": isGoogleMapsURL,
}

// checkEmbedURL checks that provider can embed s, a URL that passed checkHref.
func checkEmbedURL(provider, s string) error {
	match, ok := embedProviders[provider]
	if !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("invalid URL")
	}
	if u.Scheme != "https" {
		return errors.New("must be an https URL")
	}
	if u.Port() != "" {
		return errors.New("ports are not allowed")
	}
	// Not strings.ToLower: it maps U+0130 to "i", but browsers map it to "i" and U+0307, another domain.
	host := strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, u.Hostname())
	path := strings.Split(strings.Trim(u.Path, "/"), "/")
	if !match(host, path, u.Query()) {
		return fmt.Errorf("not a URL that %s can embed", provider)
	}
	return nil
}

func isYouTubeURL(host string, path []string, query url.Values) bool {
	switch host {
	case "youtu.be":
		return len(path) == 1 && youTubeIDPattern.MatchString(path[0])
	case "youtube.com", "www.youtube.com", "m.youtube.com", "www.youtube-nocookie.com":
		switch {
		case len(path) == 1 && path[0] == "watch":
			return youTubeIDPattern.MatchString(query.Get("v"))
		case len(path) == 2 && slices.Contains([]string{"embed", "shorts", "live"}, path[0]):
			// "/embed/videoseries?list=" embeds a playlist, not a video with that ID.
			return path[1] != "videoseries" && youTubeIDPattern.MatchString(path[1])
		}
	}
	return false
}

func isVimeoURL(host string, path []string, _ url.Values) bool {
	switch host {
	case "vimeo.com", "www.vimeo.com":
		// An unlisted video has a hash after its ID.
		return digitsPattern.MatchString(path[0]) && (len(path) == 1 || len(path) == 2 && lowerHexPattern.MatchString(path[1]))
	case "player.vimeo.com":
		return len(path) == 2 && path[0] == "video" && digitsPattern.MatchString(path[1])
	}
	return false
}

func isXURL(host string, path []string, _ url.Values) bool {
	switch host {
	case "x.com", "www.x.com", "mobile.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com":
		return len(path) == 3 && xUserPattern.MatchString(path[0]) && path[1] == "status" && digitsPattern.MatchString(path[2])
	}
	return false
}

func isGoogleMapsURL(host string, path []string, query url.Values) bool {
	return host == "www.google.com" && slices.Equal(path, []string{"maps", "embed"}) && query.Get("pb") != ""
}
