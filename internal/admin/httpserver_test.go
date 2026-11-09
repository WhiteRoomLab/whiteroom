package admin

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewHTTPServer(t *testing.T) {
	h := http.NewServeMux()
	logger := log.New(io.Discard, "", 0)
	srv := NewHTTPServer(h, logger)
	if srv.Handler != http.Handler(h) {
		t.Error("the Handler is not the one that NewHTTPServer was given")
	}
	for _, tt := range []struct {
		name      string
		got, want time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout, 10 * time.Second},
		{"ReadTimeout", srv.ReadTimeout, 30 * time.Second},
		{"WriteTimeout", srv.WriteTimeout, 60 * time.Second},
		{"IdleTimeout", srv.IdleTimeout, 120 * time.Second},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
	if srv.MaxHeaderBytes != 65536 {
		t.Errorf("MaxHeaderBytes = %d, want 65536", srv.MaxHeaderBytes)
	}
	if !srv.DisableGeneralOptionsHandler {
		t.Error("DisableGeneralOptionsHandler is false: OPTIONS * would not reach the Server")
	}
	if srv.ErrorLog != logger {
		t.Error("the ErrorLog is not the one that NewHTTPServer was given")
	}
	if srv.TLSConfig != nil || srv.Addr != "" {
		t.Error("the server has an Addr or a TLSConfig, which its caller gives")
	}

	if got := NewHTTPServer(h, nil).ErrorLog; got == nil || got.Writer() != io.Discard {
		t.Errorf("the ErrorLog of NewHTTPServer(h, nil) is %v, want one that discards", got)
	}
	if a, b := NewHTTPServer(h, nil), NewHTTPServer(h, nil); a == b {
		t.Error("NewHTTPServer returns the same server twice")
	}

	// ConnContext marks the connections over Unix domain sockets and no others.
	ctx := srv.ConnContext(context.Background(), new(net.UnixConn))
	if marked, _ := ctx.Value(unixKey{}).(bool); !marked {
		t.Error("ConnContext does not mark a connection over a Unix domain socket")
	}
	ctx = srv.ConnContext(context.Background(), new(net.TCPConn))
	if marked, _ := ctx.Value(unixKey{}).(bool); marked {
		t.Error("ConnContext marks a connection over TCP")
	}
}

func TestReadHeaderTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := NewHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}), nil)
	srv.ReadHeaderTimeout = 200 * time.Millisecond
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	// A client that sends a whole request in time is answered.
	res := rawExchange(t, l.Addr().String(), "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("GET / has the status %d, want 204", res.StatusCode)
	}

	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, "GET /api/te"); err != nil {
		t.Fatal(err)
	}
	// Whatever the server says to it, it closes the connection: ReadAll ends at the end of the stream.
	start := time.Now()
	_ = conn.SetReadDeadline(start.Add(2 * time.Second))
	reply, err := io.ReadAll(conn)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("a client that sends half a request line is still connected after %v", time.Since(start))
	}
	if len(reply) > 0 && !strings.HasPrefix(string(reply), "HTTP/1.1 400 ") {
		t.Errorf("the server answered a half request line with %q, want nothing or a 400", reply)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the handler was called %d times, want 1", got)
	}
}

func TestMaxHeaderBytes(t *testing.T) {
	var calls atomic.Int32
	addr := listen(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}), nil)
	request := func(size int) string {
		return "GET / HTTP/1.1\r\nHost: x\r\nX-Big: " + strings.Repeat("a", size) + "\r\nConnection: close\r\n\r\n"
	}

	if res := rawExchange(t, addr, request(60000)); res.StatusCode != http.StatusNoContent {
		t.Errorf("a request with 60000 bytes of headers has the status %d, want 204", res.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("the handler was called %d times, want 1", got)
	}
	if res := rawExchange(t, addr, request(80000)); res.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("a request with 80000 bytes of headers has the status %d, want 431", res.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the handler was called %d times, want 1: net/http must refuse the request first", got)
	}
}
