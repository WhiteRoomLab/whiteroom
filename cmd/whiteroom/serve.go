package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/serve"
)

const servePublicUsage = `Usage: whiteroom serve public [flags] [OUT]

Serve the published release of the output directory OUT (default "out") over
HTTP until interrupted. A release that a new build publishes is served from
the next request.

Flags:
`

// runServe runs whiteroom serve with args, those after serve.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printf(stderr, "%s", usage)
		return 2
	}
	switch args[0] {
	case "public":
		return servePublic(ctx, args[1:], stdout, stderr)
	case "admin":
		printf(stderr, "whiteroom serve: admin is not available yet\n")
		return 2
	case "help", "-h", "-help", "--help":
		printf(stdout, "%s", usage)
		return 0
	}
	printf(stderr, "whiteroom serve: unknown server %q\nRun \"whiteroom help\" for usage.\n", args[0])
	return 2
}

// servePublic runs whiteroom serve public with args, those after public, until ctx is done.
func servePublic(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("whiteroom serve public", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", servePublicUsage)
		flags.PrintDefaults()
	}
	var addr string
	flags.StringVar(&addr, "addr", "127.0.0.1:8080", "listen on the TCP address `ADDR`, host:port")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		return usageError(flags, "too many arguments: give at most one OUT, after the flags")
	}
	out := cmp.Or(flags.Arg(0), "out")
	fail := func(err error) int {
		printf(stderr, "whiteroom serve public: %v\n", err)
		return 1
	}

	logger := log.New(stderr, "", log.LstdFlags)
	h, err := serve.New(out, logger)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = h.Close() }()
	base, err := h.Base()
	if err != nil {
		return fail(err)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fail(err)
	}
	printf(stdout, "serving %s at %s\n", out, siteURL(l.Addr().String(), base))

	// ReadTimeout also ends requests whose bodies, which the server never needs, do not come. No WriteTimeout, which
	// would cut the downloads of large files on slow connections.
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          logger,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(l) }()
	select {
	case err := <-errc:
		_ = srv.Close()
		return fail(err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		_ = srv.Close()
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fail(err)
	}
	return 0
}

// siteURL returns the URL of the site whose path is base, in the canonical form of froms, at addr, the address of a
// listener: with localhost for an unspecified IP, which means every interface, and with the bytes in base that URLs
// cannot have percent-encoded: those of non-ASCII characters, spaces and controls.
func siteURL(addr, base string) string {
	if host, port, err := net.SplitHostPort(addr); err == nil && net.ParseIP(host).IsUnspecified() {
		addr = net.JoinHostPort("localhost", port)
	}
	var b []byte
	for _, c := range []byte(base) {
		if c <= ' ' || c >= 0x7f {
			b = fmt.Appendf(b, "%%%02X", c)
		} else {
			b = append(b, c)
		}
	}
	return "http://" + addr + string(b) + "/"
}
