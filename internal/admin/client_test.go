package admin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// clientRequest returns a request from the peer remote, with the lines of X-Forwarded-For. It has X-Real-IP and
// Forwarded as well, which are not read, with addresses that no test expects. It is over a Unix domain socket if unix.
func clientRequest(remote string, unix bool, lines ...string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "https://admin.example.com/api/addr", nil)
	req.RemoteAddr = remote
	for _, line := range lines {
		req.Header.Add("X-Forwarded-For", line)
	}
	req.Header.Set("X-Real-IP", "198.18.0.1")
	req.Header.Set("Forwarded", "for=198.18.0.2")
	if unix {
		req = req.WithContext(markUnix(req.Context(), new(net.UnixConn)))
	}
	return req
}

func TestClientAddr(t *testing.T) {
	ts := newTestServer(t, httpsOrigin, "10.0.0.0/8", "172.16.0.0/12", "2001:db8:ffff::/48")
	const (
		untrusted = "192.0.2.1:1234"
		proxy     = "10.1.2.3:4567"
	)
	for _, tt := range []struct {
		name   string
		remote string
		unix   bool
		lines  []string
		want   string
	}{
		{"an untrusted peer", untrusted, false, nil, "192.0.2.1"},
		{"a forged header from an untrusted peer", untrusted, false, []string{"203.0.113.9"}, "192.0.2.1"},
		{"a forged chain from an untrusted peer", untrusted, false, []string{"10.5.5.5, 203.0.113.9"}, "192.0.2.1"},
		{"an untrusted IPv6 peer", "[2001:db8:1::5]:80", false, []string{"203.0.113.9"}, "2001:db8:1::5"},
		{"a trusted peer without the header", proxy, false, nil, "10.1.2.3"},
		{"one element", proxy, false, []string{"203.0.113.9"}, "203.0.113.9"},
		{"the last of two", proxy, false, []string{"198.51.100.7, 203.0.113.9"}, "203.0.113.9"},
		{"the last of three", proxy, false, []string{"1.1.1.1, 2.2.2.2, 203.0.113.9"}, "203.0.113.9"},
		{"trusted proxies at the end", proxy, false, []string{"203.0.113.9, 10.9.9.9, 172.16.0.1"}, "203.0.113.9"},
		{"a trusted proxy in the middle", proxy, false, []string{"198.51.100.7, 10.9.9.9, 203.0.113.9"}, "203.0.113.9"},
		{"all trusted: the first", proxy, false, []string{"10.5.5.5, 10.6.6.6, 10.7.7.7"}, "10.5.5.5"},
		{"one trusted element", proxy, false, []string{"10.5.5.5"}, "10.5.5.5"},
		{"an IPv4-mapped peer", "[::ffff:10.1.2.3]:80", false, []string{"203.0.113.9"}, "203.0.113.9"},
		{"an IPv4-mapped untrusted peer", "[::ffff:192.0.2.1]:80", false, []string{"203.0.113.9"}, "192.0.2.1"},
		{"an IPv4-mapped client", proxy, false, []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
		{"an IPv4-mapped trusted client", proxy, false, []string{"203.0.113.9, ::ffff:10.2.2.2"}, "203.0.113.9"},
		{"an IPv6 peer in a prefix", "[2001:db8:ffff::1]:80", false, []string{"2001:db8:1::5"}, "2001:db8:1::5"},
		{"the zone of the peer", "[fe80::1%eth0]:80", false, nil, "fe80::1"},
		{"the zone of the client", proxy, false, []string{"fe80::2%eth0"}, "fe80::2"},
		{"a port", proxy, false, []string{"203.0.113.9:4711"}, "203.0.113.9"},
		{"an IPv6 address with a port", proxy, false, []string{"[2001:db8:1::9]:4711"}, "2001:db8:1::9"},
		{"an IPv6 address", proxy, false, []string{"2001:db8:1::9"}, "2001:db8:1::9"},
		{"spaces and tabs", proxy, false, []string{"  198.51.100.7 ,\t 203.0.113.9 \t"}, "203.0.113.9"},
		{"two lines", proxy, false, []string{"198.51.100.7", "1.1.1.1, 203.0.113.9"}, "203.0.113.9"},
		{"two lines, the last trusted", proxy, false, []string{"203.0.113.9", "10.2.2.2"}, "203.0.113.9"},
		{"two lines, all trusted", proxy, false, []string{"10.5.5.5", "10.6.6.6"}, "10.5.5.5"},
		{"three lines", proxy, false, []string{"1.1.1.1", "2.2.2.2", "203.0.113.9"}, "203.0.113.9"},
		{"garbage at the end", proxy, false, []string{"203.0.113.9, garbage"}, "::"},
		{"garbage before the client is not read", proxy, false, []string{"garbage, 203.0.113.9"}, "203.0.113.9"},
		{"garbage past a trusted proxy", proxy, false, []string{"garbage, 10.2.2.2"}, "::"},
		{"garbage in the first line", proxy, false, []string{"garbage", "10.2.2.2"}, "::"},
		{"an empty element", proxy, false, []string{"203.0.113.9,"}, "::"},
		{"an empty line", proxy, false, []string{""}, "::"},
		{"an empty first line", proxy, false, []string{"", "203.0.113.9"}, "203.0.113.9"},
		{"brackets without a port", proxy, false, []string{"[2001:db8:1::9]"}, "::"},
		{"a port without an address", proxy, false, []string{":80"}, "::"},
		{"an address and a name", proxy, false, []string{"203.0.113.9 example.com"}, "::"},
		{"a prefix", proxy, false, []string{"203.0.113.0/24"}, "::"},
		{"a peer without a port", "10.1.2.3", false, []string{"203.0.113.9"}, "::"},
		{"an empty peer", "", false, []string{"203.0.113.9"}, "::"},
		{"a peer that is a name", "localhost:80", false, []string{"203.0.113.9"}, "::"},
		{"a Unix peer without the header", "", true, nil, "::"},
		{"a Unix peer with an address", "192.0.2.1:1", true, nil, "::"},
		{"a Unix peer with the header", "", true, []string{"203.0.113.9"}, "203.0.113.9"},
		{"a Unix peer with a chain", "", true, []string{"198.51.100.7, 203.0.113.9"}, "203.0.113.9"},
		{"a Unix peer with trusted proxies", "", true, []string{"198.51.100.7, 10.9.9.9"}, "198.51.100.7"},
		{"a Unix peer with trusted clients", "", true, []string{"10.5.5.5, 10.9.9.9"}, "10.5.5.5"},
		{"a Unix peer with garbage", "", true, []string{"garbage"}, "::"},
	} {
		got := ts.clientAddr(clientRequest(tt.remote, tt.unix, tt.lines...))
		if got.String() != tt.want {
			t.Errorf("%s: clientAddr = %v, want %s", tt.name, got, tt.want)
		}
		if got.Is4In6() || got.Zone() != "" || !got.IsValid() {
			t.Errorf("%s: clientAddr = %#v is not a plain address", tt.name, got)
		}
	}

	// Without trusted proxies, only a Unix peer is believed.
	plain := newTestServer(t, httpsOrigin)
	for _, tt := range []struct {
		remote string
		unix   bool
		want   string
	}{
		{proxy, false, "10.1.2.3"},
		{untrusted, false, "192.0.2.1"},
		{"", true, "203.0.113.9"},
	} {
		if got := plain.clientAddr(clientRequest(tt.remote, tt.unix, "203.0.113.9")); got.String() != tt.want {
			t.Errorf("without trusted proxies, clientAddr(%q, unix %v) = %v, want %s", tt.remote, tt.unix, got, tt.want)
		}
	}
}

func TestClientPrefix(t *testing.T) {
	for _, tt := range []struct{ addr, want string }{
		{"192.0.2.77", "192.0.2.77/32"},
		{"0.0.0.0", "0.0.0.0/32"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff:ffff:ffff:ffff", "2001:db8:1:2::/64"},
		{"::", "::/64"},
		{"::ffff:192.0.2.77", "192.0.2.77/32"},
		{"fe80::1%eth0", "fe80::/64"},
	} {
		got := clientPrefix(netip.MustParseAddr(tt.addr))
		if got.String() != tt.want {
			t.Errorf("clientPrefix(%s) = %v, want %s", tt.addr, got, tt.want)
		}
	}
	if got := clientPrefix(netip.Addr{}); got.IsValid() {
		t.Errorf("clientPrefix of the zero Addr = %v, want the zero Prefix", got)
	}
}

// TestUnixSocket serves over a Unix domain socket, whose peer is always a proxy.
func TestUnixSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the socket is an abstract one, which Linux has")
	}
	ts := newTestServer(t, localOrigin, "10.0.0.0/8")
	name := fmt.Sprintf("@whiteroom-admin-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	l, err := net.Listen("unix", name)
	if err != nil {
		t.Skipf("no abstract Unix socket: %v", err)
	}
	srv := NewHTTPServer(ts.Server, nil)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", name)
		},
	}}
	t.Cleanup(client.CloseIdleConnections)

	for _, tt := range []struct {
		lines []string
		want  string
	}{
		{nil, "::"},
		{[]string{"203.0.113.9"}, "203.0.113.9"},
		{[]string{"198.51.100.7, 203.0.113.9"}, "203.0.113.9"},
		{[]string{"203.0.113.9, 10.1.1.1"}, "203.0.113.9"},
		{[]string{"198.51.100.7", "203.0.113.9"}, "203.0.113.9"},
		{[]string{"garbage"}, "::"},
	} {
		req, err := http.NewRequest(http.MethodGet, "http://localhost:8081/api/addr", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range tt.lines {
			req.Header.Add("X-Forwarded-For", line)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := bodyOf(t, res), fmt.Sprintf("%q", tt.want); got != want {
			t.Errorf("over a Unix socket with %q, the client is %s, want %s", strings.Join(tt.lines, " | "), got, want)
		}
	}

	// Over TCP, the header is read for a peer in the prefixes, and for no other.
	for _, tt := range []struct {
		trusted string
		want    string
	}{
		{"", `"127.0.0.1"`},
		{"127.0.0.0/8", `"203.0.113.9"`},
	} {
		var proxies []string
		if tt.trusted != "" {
			proxies = append(proxies, tt.trusted)
		}
		tcp := newTestServer(t, localOrigin, proxies...)
		addr := listen(t, tcp.Server, nil)
		res := rawExchange(t, addr, "GET /api/addr HTTP/1.1\r\nHost: localhost:8081\r\nX-Forwarded-For: 203.0.113.9\r\n"+
			"Connection: close\r\n\r\n")
		if got := bodyOf(t, res); got != tt.want {
			t.Errorf("over TCP with the trusted proxy %q, the client is %s, want %s", tt.trusted, got, tt.want)
		}
	}
}

// refClientAddr is clientAddr the long way: it collects the elements of the header first.
func refClientAddr(s *Server, remote string, unix bool, lines []string) netip.Addr {
	var peer netip.Addr
	if !unix {
		ap, err := netip.ParseAddrPort(remote)
		if err != nil {
			return unspecified
		}
		peer = canonicalAddr(ap.Addr())
		if !s.trusted(peer) {
			return peer
		}
	}
	var elements []string
	for _, line := range lines {
		for _, element := range strings.Split(line, ",") {
			elements = append(elements, strings.Trim(element, " \t"))
		}
	}
	if len(elements) == 0 {
		if unix {
			return unspecified
		}
		return peer
	}
	for i := len(elements) - 1; i >= 0; i-- {
		a, ok := parseClient(elements[i])
		if !ok {
			return unspecified
		}
		if !s.trusted(a) || i == 0 {
			return a
		}
	}
	panic("unreachable")
}

// FuzzClientAddr checks that clientAddr always returns a plain, valid address, with clientPrefix, and that it is what
// the long way gives.
func FuzzClientAddr(f *testing.F) {
	s, err := New(testConfig(httpsOrigin,
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8:ffff::/48"), netip.MustParsePrefix("192.0.2.0/24")))
	if err != nil {
		f.Fatal(err)
	}
	f.Add("192.0.2.1:1234", "203.0.113.9", "", uint8(1), false)
	f.Add("10.1.2.3:80", "198.51.100.7, 203.0.113.9", "10.2.2.2", uint8(2), false)
	f.Add("[::ffff:10.1.2.3]:80", "::ffff:203.0.113.9", "", uint8(1), false)
	f.Add("[fe80::1%eth0]:80", "fe80::2%eth0, [2001:db8::1]:80", " \t,", uint8(2), false)
	f.Add("", "203.0.113.9:80, garbage", "", uint8(1), true)
	f.Add("garbage", "", "", uint8(0), false)
	f.Add("10.1.2.3:80", ",,,", ",", uint8(2), false)
	f.Fuzz(func(t *testing.T, remote, line1, line2 string, n uint8, unix bool) {
		lines := []string{line1, line2}[:n%3]
		req := &http.Request{Header: make(http.Header), RemoteAddr: remote}
		for _, line := range lines {
			req.Header.Add("X-Forwarded-For", line)
		}
		if unix {
			req = req.WithContext(markUnix(req.Context(), new(net.UnixConn)))
		}
		got := s.clientAddr(req)
		if !got.IsValid() || got.Is4In6() || got.Zone() != "" {
			t.Fatalf("clientAddr(%q, %q, unix %v) = %#v, which is not a plain address", remote, lines, unix, got)
		}
		if want := refClientAddr(s, remote, unix, lines); got != want {
			t.Fatalf("clientAddr(%q, %q, unix %v) = %v, the long way gives %v", remote, lines, unix, got, want)
		}
		p := clientPrefix(got)
		if !p.IsValid() || p.Masked() != p || !p.Contains(got) || (p.Bits() != 32 && p.Bits() != 64) {
			t.Fatalf("clientPrefix(%v) = %v", got, p)
		}
	})
}
