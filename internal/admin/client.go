package admin

import (
	"context"
	"iter"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// unixKey is the key of the context value that marks a connection over a Unix domain socket.
type unixKey struct{}

// markUnix is the ConnContext of the servers of this package. The peer of a Unix domain socket is a reverse proxy
// that the file permissions of the socket let in, so it is always trusted.
func markUnix(ctx context.Context, c net.Conn) context.Context {
	if _, ok := c.(*net.UnixConn); ok {
		return context.WithValue(ctx, unixKey{}, true)
	}
	return ctx
}

// unspecified is the address of a client that cannot be told: they all share it, and so the limits that count by
// address.
var unspecified = netip.IPv6Unspecified()

// clientAddr returns the address of the client of r, which is never zero or an IPv4-mapped IPv6 address, and has no
// zone. It is the address of the peer of the connection, unless the peer is a trusted proxy: one of the
// TrustedProxies, or any peer of a Unix domain socket. Then it is the last address of X-Forwarded-For that is not
// trusted, which the first trusted proxy has added, or else the first. Forwarded and X-Real-IP are not read. If the
// peer or an address that the walk comes to cannot be read, or if a Unix peer sends no X-Forwarded-For, it is
// unspecified.
func (s *Server) clientAddr(r *http.Request) netip.Addr {
	unix, _ := r.Context().Value(unixKey{}).(bool)
	var peer netip.Addr
	if !unix {
		ap, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			return unspecified
		}
		peer = canonicalAddr(ap.Addr())
		if !s.trusted(peer) {
			s.noteForwardedFor(r)
			return peer
		}
	}
	var client netip.Addr
	for element := range forwardedFor(r.Header) {
		a, ok := parseClient(element)
		if !ok {
			return unspecified
		}
		if !s.trusted(a) {
			return a
		}
		client = a
	}
	switch {
	case client.IsValid():
		return client
	case unix:
		return unspecified
	}
	return peer
}

// noteForwardedFor logs, at most once a minute, that r has an X-Forwarded-For header that is not read, since its peer is not
// a trusted proxy: the usual cause is a reverse proxy that TrustedProxies does not list, and then every client counts as the
// proxy. The line holds nothing of the request.
func (s *Server) noteForwardedFor(r *http.Request) {
	if len(r.Header.Values("X-Forwarded-For")) > 0 && s.forwardedDue() {
		s.log.Print("X-Forwarded-For comes from a peer that is not in TrustedProxies, and is not read; " +
			"if a reverse proxy sends it, add the proxy to TrustedProxies")
	}
}

func (s *Server) trusted(a netip.Addr) bool {
	return slices.ContainsFunc(s.proxies, func(p netip.Prefix) bool { return p.Contains(a) })
}

// canonicalAddr returns a as IPv4 if it is IPv4-mapped, and without a zone.
func canonicalAddr(a netip.Addr) netip.Addr { return a.Unmap().WithZone("") }

// parseClient reads an element of X-Forwarded-For, an address or an address with a port.
func parseClient(s string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		return canonicalAddr(a), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return canonicalAddr(ap.Addr()), true
	}
	return netip.Addr{}, false
}

// forwardedFor yields the elements of all the X-Forwarded-For lines of h, from the last to the first, without the
// spaces around them. A line yields at least one, which is empty if the line is.
func forwardedFor(h http.Header) iter.Seq[string] {
	return func(yield func(string) bool) {
		lines := h.Values("X-Forwarded-For")
		for i := len(lines) - 1; i >= 0; i-- {
			line := lines[i]
			for {
				cut := strings.LastIndexByte(line, ',')
				if !yield(strings.Trim(line[cut+1:], " \t")) {
					return
				}
				if cut < 0 {
					break
				}
				line = line[:cut]
			}
		}
	}
}

// clientPrefix returns the range of addresses that share a limit with a: a itself for IPv4, and its /64 for IPv6,
// which one customer usually has. It is the zero Prefix for the zero Addr.
func clientPrefix(a netip.Addr) netip.Prefix {
	a = canonicalAddr(a)
	bits := 64
	if a.Is4() {
		bits = 32
	}
	p, _ := a.Prefix(bits)
	return p
}
