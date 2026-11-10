package admin

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// TestNewTrustedProxiesNeedFixedBits shows that New refuses a prefix with no fixed bits, which would trust the header of any
// peer, and that it names the prefix as it does for one that is not valid.
func TestNewTrustedProxiesNeedFixedBits(t *testing.T) {
	m, _ := testAccounts(t)
	for _, tt := range []struct{ prefix, want string }{
		{"0.0.0.0/0", "admin: invalid trusted proxy 0.0.0.0/0: it covers every address"},
		{"::/0", "admin: invalid trusted proxy ::/0: it covers every address"},
		{"10.0.0.0/0", "admin: invalid trusted proxy 10.0.0.0/0: it covers every address"},
		{"2001:db8::/0", "admin: invalid trusted proxy 2001:db8::/0: it covers every address"},
	} {
		p := netip.MustParsePrefix(tt.prefix)
		valid := netip.MustParsePrefix("10.0.0.0/8")
		for _, proxies := range [][]netip.Prefix{{p}, {valid, p}, {p, valid}} {
			_, err := New(testConfig(m, httpsOrigin, proxies...))
			if err == nil || err.Error() != tt.want {
				t.Errorf("New with the trusted proxies %v = %v, want the error %q", proxies, err, tt.want)
			}
		}
	}

	// A prefix that is not valid is refused first, in the order of the list, and the prefixes of one fixed bit and more are fine.
	_, err := New(testConfig(m, httpsOrigin, netip.MustParsePrefix("0.0.0.0/0"), netip.Prefix{}))
	if want := "admin: invalid trusted proxy 0.0.0.0/0: it covers every address"; err == nil || err.Error() != want {
		t.Errorf("New with 0.0.0.0/0 and a prefix that is not valid = %v, want the error %q", err, want)
	}
	_, err = New(testConfig(m, httpsOrigin, netip.Prefix{}, netip.MustParsePrefix("0.0.0.0/0")))
	if want := "admin: invalid trusted proxy invalid Prefix"; err == nil || err.Error() != want {
		t.Errorf("New with a prefix that is not valid and 0.0.0.0/0 = %v, want the error %q", err, want)
	}
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", "10.0.0.0/8", "192.0.2.1/32", "2001:db8::/32", "::1/128"} {
		if _, err := New(testConfig(m, httpsOrigin, netip.MustParsePrefix(prefix))); err != nil {
			t.Errorf("New with the trusted proxy %s = %v, want it to be accepted", prefix, err)
		}
	}
}

// TestForwardedForFromAnUntrustedPeer shows the line of the log for a request that has an X-Forwarded-For header from a peer
// that is not a trusted proxy, which is what a reverse proxy that TrustedProxies does not list looks like.
func TestForwardedForFromAnUntrustedPeer(t *testing.T) {
	const line = "X-Forwarded-For comes from a peer that is not in TrustedProxies, and is not read; " +
		"if a reverse proxy sends it, add the proxy to TrustedProxies\n"
	ts := newTestServer(t, httpsOrigin, "10.0.0.0/8")

	// The address is the peer's, and the line says nothing of the request.
	if got := ts.clientAddr(clientRequest("192.0.2.1:1234", false, "203.0.113.9")); got.String() != "192.0.2.1" {
		t.Errorf("clientAddr = %v, want the address of the peer, 192.0.2.1", got)
	}
	if got := ts.logs.String(); got != line {
		t.Errorf("the log says %q, want %q", got, line)
	}

	// At most one a minute, by the clock of Config.Now: no matter how many requests, or what they carry.
	for i, lines := range [][]string{{"203.0.113.10"}, {"198.51.100.1, 203.0.113.11", "garbage"}, {""}} {
		ts.clientAddr(clientRequest(fmt.Sprintf("192.0.2.%d:1234", 2+i), false, lines...))
	}
	ts.clock.advance(time.Minute - 1)
	ts.clientAddr(clientRequest("192.0.2.9:1234", false, "203.0.113.12"))
	if got := ts.logs.String(); got != line {
		t.Errorf("the log within the minute says %q, want the one line", got)
	}
	ts.clock.advance(1)
	ts.clientAddr(clientRequest("192.0.2.9:1234", false, "203.0.113.12"))
	ts.clientAddr(clientRequest("192.0.2.9:1234", false, "203.0.113.12"))
	if got := ts.logs.String(); got != line+line {
		t.Errorf("the log after the minute says %q, want two lines", got)
	}

	// A header that is empty is a header too, and so are two of them.
	ts.clock.advance(time.Minute)
	ts.clientAddr(clientRequest("192.0.2.9:1234", false, ""))
	ts.clock.advance(time.Minute)
	ts.clientAddr(clientRequest("[2001:db8::1]:1234", false, "203.0.113.12", "203.0.113.13"))
	if got := ts.logs.String(); got != line+line+line+line {
		t.Errorf("the log after an empty header and two headers says %q, want four lines", got)
	}

	// Nothing is logged where the header is believed or where it is not there to be read, though a line is due.
	for _, tt := range []struct {
		what   string
		remote string
		unix   bool
		lines  []string
	}{
		{"a trusted proxy", "10.1.2.3:4567", false, []string{"203.0.113.9"}},
		{"a trusted proxy and a chain", "10.1.2.3:4567", false, []string{"203.0.113.9, 10.9.9.9"}},
		{"a Unix socket", "", true, []string{"203.0.113.9"}},
		{"an untrusted peer without the header", "192.0.2.1:1234", false, nil},
		{"a peer that cannot be read", "garbage", false, []string{"203.0.113.9"}},
		{"a peer without a port", "192.0.2.1", false, []string{"203.0.113.9"}},
	} {
		ts.clock.advance(time.Minute)
		before := ts.logs.String()
		ts.clientAddr(clientRequest(tt.remote, tt.unix, tt.lines...))
		if got := ts.logs.String(); got != before {
			t.Errorf("%s: the log has the new line %q", tt.what, got[len(before):])
		}
	}
}

// TestForwardedForFromAnUntrustedPeerOverHTTP shows it in a login, which reads the address: the answer is the one for the peer.
func TestForwardedForFromAnUntrustedPeerOverHTTP(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	for range freeAttempts {
		ts.wantFailure(t, "a wrong password", "admin", wrongPassword, join(from("192.0.2.1:4000"), viaProxy("203.0.113.9"))...)
	}
	// The address of the header was not read: the pair of the peer waits, whatever the header says.
	ts.wantRetry(t, "a sixth attempt with another address in the header", "admin", wrongPassword, "30",
		join(from("192.0.2.1:4000"), viaProxy("203.0.113.10"))...)
	if got, want := ts.logs.String(), "X-Forwarded-For comes from a peer that is not in TrustedProxies, and is not read; "+
		"if a reverse proxy sends it, add the proxy to TrustedProxies\n"; got != want {
		t.Errorf("the log says %q, want one line: %q", got, want)
	}
}
