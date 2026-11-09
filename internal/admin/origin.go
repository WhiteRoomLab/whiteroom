package admin

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

const (
	reasonScheme = "use https, or http on localhost"
	reasonParts  = "use only a scheme, a host and a port"
	reasonNoHost = "it has no host"
	reasonPort   = "invalid port"
)

// origin is Config.Origin, parsed and in the form that browsers send in the Origin header.
type origin struct {
	scheme string // "http" or "https"
	host   string // in lower case; an IPv6 address in brackets
	port   string // "" for the default port of the scheme
}

func (o origin) String() string {
	s := o.scheme + "://" + o.host
	if o.port != "" {
		s += ":" + o.port
	}
	return s
}

// hosts returns the values of the Host header that name the origin, in lower case: browsers leave out the default port
// of the scheme.
func (o origin) hosts() []string {
	if o.port != "" {
		return []string{o.host + ":" + o.port}
	}
	return []string{o.host, o.host + ":" + defaultPort(o.scheme)}
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func parseOrigin(value string) (origin, error) {
	o, reason := parseOriginValue(value)
	if reason != "" {
		return origin{}, fmt.Errorf("admin: invalid origin %q: %s", value, reason)
	}
	return o, nil
}

// parseOriginValue returns the origin that value names, or the reason why it names none. The checks are in this order:
// the scheme, the shape of the rest, the host being there, the port, the host, and then http being on localhost.
func parseOriginValue(value string) (origin, string) {
	scheme, rest, found := strings.Cut(value, "://")
	scheme = strings.ToLower(scheme)
	if !found || (scheme != "http" && scheme != "https") {
		return origin{}, reasonScheme
	}
	rest = strings.TrimSuffix(rest, "/")
	if !isHostPortText(rest) {
		return origin{}, reasonParts
	}
	hp, ok := splitHostPort(rest)
	if !ok {
		return origin{}, reasonParts
	}
	if hp.host == "" {
		return origin{}, reasonNoHost
	}
	o := origin{scheme: scheme}
	if hp.hasPort {
		port, ok := parsePort(hp.port)
		if !ok {
			return origin{}, reasonPort
		}
		if port != defaultPort(scheme) {
			o.port = port
		}
	}
	host, addr, ok := parseHost(hp.host)
	if !ok {
		return origin{}, reasonParts
	}
	o.host = host
	if scheme == "http" && !isLoopback(host, addr) {
		return origin{}, reasonScheme
	}
	return o, ""
}

// isHostPortText reports whether s has only the bytes that a host with a port can have: letters, digits, "-", "_",
// ".", ":", "[" and "]".
func isHostPortText(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '_', c == '.', c == ':', c == '[', c == ']':
		default:
			return false
		}
	}
	return true
}

// hostPort is a host and a port, as text; hasPort is true for an empty port that follows a colon.
type hostPort struct {
	host, port string
	hasPort    bool
}

// splitHostPort splits s, which isHostPortText accepts, at the colon before the port. An IPv6 address must be in
// brackets, which the host keeps. ok is false if s has colons that are not those of an address in brackets and its
// port.
func splitHostPort(s string) (hp hostPort, ok bool) {
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return hostPort{}, false
		}
		hp.host = s[:end+1]
		if end+1 == len(s) {
			return hp, true
		}
		hp.port, hp.hasPort = s[end+2:], true
		return hp, s[end+1] == ':'
	}
	switch strings.Count(s, ":") {
	case 0:
		return hostPort{host: s}, true
	case 1:
		host, port, _ := strings.Cut(s, ":")
		return hostPort{host: host, port: port, hasPort: true}, true
	}
	return hostPort{}, false
}

// parsePort returns the port in its canonical form, with no leading zero.
func parsePort(s string) (string, bool) {
	if !isDigits(s) {
		return "", false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return strconv.Itoa(n), true
}

// parseHost returns the host in the form that browsers send, and its address if it is one. A host whose last label is
// a number must be an IPv4 address in four decimal parts: browsers read "127.1" as 127.0.0.1.
func parseHost(text string) (string, netip.Addr, bool) {
	if inner, found := strings.CutPrefix(text, "["); found {
		inner, found = strings.CutSuffix(inner, "]")
		addr, err := netip.ParseAddr(inner)
		if !found || err != nil || !addr.Is6() {
			return "", netip.Addr{}, false
		}
		return "[" + addr.String() + "]", addr, true
	}
	text = strings.ToLower(text)
	if isDigits(text[strings.LastIndexByte(text, '.')+1:]) {
		addr, err := netip.ParseAddr(text)
		if err != nil || !addr.Is4() {
			return "", netip.Addr{}, false
		}
		return addr.String(), addr, true
	}
	if !isDomainName(text) {
		return "", netip.Addr{}, false
	}
	return text, netip.Addr{}, true
}

// isDomainName reports whether s, in lower case, is made of labels of at most 63 letters, digits, "-" and "_", and is
// at most 253 bytes long.
func isDomainName(s string) bool {
	if len(s) > 253 {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := range len(label) {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

func isDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isLoopback reports whether host is localhost, an IPv4 address in 127.0.0.0/8 or [::1], where http is allowed.
func isLoopback(host string, addr netip.Addr) bool {
	switch {
	case !addr.IsValid():
		return host == "localhost"
	case addr.Is4():
		return addr.As4()[0] == 127
	}
	return addr == netip.IPv6Loopback()
}
