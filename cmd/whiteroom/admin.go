package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/admin"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/publish"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const serveAdminUsage = `Usage: whiteroom serve admin [flags] [SITE]

Open the data directory as the process that writes it, and serve its admin API
over HTTP until interrupted. The site is built from the data directory into the
output directory when the server starts, and after each change that publishes.
From the site directory SITE (default ".") only the schema file site/schema.yaml
and the theme are read.

The server does not end TLS. It listens on one of a loopback TCP address (-addr)
and a Unix domain socket (-socket): browsers open the first over http on
localhost, or through a reverse proxy that ends TLS, whose address
-trusted-proxy names, so that the login history has the addresses of the clients.
The reverse proxy for the second connects to the socket file, which the server
makes with the mode 0660 and removes when it stops, and is always trusted. A
socket has no address that the origin can be made of, so it needs -origin.

With -config, the items data, site, theme, out, cache, workers, memory, addr,
socket, origin and trustedProxies of the YAML file give what the flags and SITE
leave out. Without it, no config file is read.

Flags:
`

const (
	defaultAdminAddr  = "127.0.0.1:8081"
	adminShutdownWait = 10 * time.Second
)

// serveAdminFlags are the values of the flags of whiteroom serve admin, those of whiteroom build among them.
type serveAdminFlags struct {
	*buildFlags
	data, addr, socket, origin string
	proxies                    []netip.Prefix // -trusted-proxy
}

func newServeAdminFlags(stderr io.Writer) (*flag.FlagSet, *serveAdminFlags) {
	flags := flag.NewFlagSet("whiteroom serve admin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", serveAdminUsage)
		flags.PrintDefaults()
	}
	f := &serveAdminFlags{buildFlags: addBuildFlags(flags)}
	flags.StringVar(&f.data, "data", "", "open the data directory `DIR` (required unless the config file has data)")
	flags.StringVar(&f.addr, "addr", "",
		"listen on the loopback TCP address `ADDR`, an IP address and a port; not with -socket (default addr of the config "+
			"file, else "+defaultAdminAddr+")")
	flags.StringVar(&f.socket, "socket", "",
		"listen on the Unix domain socket at `PATH` instead of a TCP address, which needs -origin; not with -addr "+
			"(default socket of the config file)")
	flags.StringVar(&f.origin, "origin", "",
		"the origin `URL` that browsers open: https, or http on localhost (default origin of the config file, else, for a "+
			"TCP address, http:// and the address that it listens on)")
	flags.Func("trusted-proxy",
		"trust the X-Forwarded-For header of the reverse proxy at `PREFIX`, an IP address or a CIDR range; repeat it for "+
			"more (default trustedProxies of the config file)",
		func(s string) error {
			p, err := parseProxy(s)
			if err != nil {
				return err
			}
			f.proxies = append(f.proxies, p)
			return nil
		})
	return flags, f
}

// adminSettings is what whiteroom serve admin works with.
type adminSettings struct {
	buildSettings
	data    string
	network string // "tcp" or "unix"
	addr    string // to listen on: a host:port, or the path of the socket
	origin  string // "" for the one that the listening address makes, which only a TCP address can
	proxies []netip.Prefix
}

// resolve returns the settings from the flags, parsed into f, and the argument of flags, over those of cfg, over the
// defaults. If one is wrong it prints it on stderr and returns false, which is a usage error.
func (f *serveAdminFlags) resolve(flags *flag.FlagSet, cfg config, stderr io.Writer) (adminSettings, bool) {
	s := adminSettings{
		buildSettings: f.settings(flags, cfg), data: cmp.Or(f.data, cfg.data), origin: cmp.Or(f.origin, cfg.origin),
	}
	if s.data == "" {
		usageError(flags, "no data directory: give -data DIR, or data in the config file")
		return s, false
	}
	choice, err := chooseAdminListen(f.addr, flagGiven(flags, "addr"), f.socket, flagGiven(flags, "socket"), cfg.addr, cfg.socket)
	switch {
	case errors.Is(err, errBothFlags):
		usageError(flags, "%v", err)
		return s, false
	case err != nil:
		printConfigProblem(stderr, f.config, "", err.Error())
		return s, false
	}
	s.network, s.addr = choice.network, choice.address
	switch {
	case s.network == "tcp":
		if err := checkAdminAddr(s.addr); err != nil {
			if choice.fromConfig {
				printConfigProblem(stderr, f.config, "/addr", err.Error())
			} else {
				usageError(flags, "-addr %v", err)
			}
			return s, false
		}
	case s.addr == "":
		usageError(flags, "-socket must not be empty")
		return s, false
	case s.origin == "":
		usageError(flags, "no origin: a socket has no address that the origin can be made of: give -origin URL, or origin in "+
			"the config file")
		return s, false
	}
	if len(f.proxies) > 0 {
		s.proxies = f.proxies
		return s, true
	}
	ok := true
	for i, text := range cfg.trustedProxies {
		p, err := parseProxy(text)
		if err != nil {
			printConfigProblem(stderr, f.config, "/trustedProxies/"+strconv.Itoa(i), fmt.Sprintf("%q: %v", text, err))
			ok = false
			continue
		}
		s.proxies = append(s.proxies, p)
	}
	return s, ok
}

// printConfigProblem prints on w the problem msg with the item at path in the config file.
func printConfigProblem(w io.Writer, file, path, msg string) {
	printf(w, "%s\n", (&content.Error{File: file, Path: path, Msg: msg}).Error())
}

// listenChoice is what the server listens on, and whether the config file said so.
type listenChoice struct {
	network    string // "tcp", and address is a host:port; or "unix", and it is the path of the socket
	address    string
	fromConfig bool
}

var (
	errBothFlags  = errors.New("-addr and -socket are both given: give one of them, as the server listens on one")
	errBothConfig = errors.New("addr and socket are both given: give one of them, as the server listens on one")
)

// chooseAdminListen returns what to listen on: what the command line gives, -addr or -socket, which are not both given,
// and then the config file is not read for it; else that of the config file, which does not give both items either; else
// the default TCP address.
func chooseAdminListen(flagAddr string, addrGiven bool, flagSocket string, socketGiven bool, cfgAddr, cfgSocket string) (listenChoice, error) {
	switch {
	case addrGiven && socketGiven:
		return listenChoice{}, errBothFlags
	case addrGiven:
		return listenChoice{network: "tcp", address: flagAddr}, nil
	case socketGiven:
		return listenChoice{network: "unix", address: flagSocket}, nil
	case cfgAddr != "" && cfgSocket != "":
		return listenChoice{}, errBothConfig
	case cfgSocket != "":
		return listenChoice{network: "unix", address: cfgSocket, fromConfig: true}, nil
	case cfgAddr != "":
		return listenChoice{network: "tcp", address: cfgAddr, fromConfig: true}, nil
	}
	return listenChoice{network: "tcp", address: defaultAdminAddr}, nil
}

// checkAdminAddr checks that addr is the IP address of a loopback interface, 127.0.0.0/8 or ::1, with a port: the host
// is not a name, which may resolve to another address, and not an unspecified address, or an IPv4-mapped one or one with a
// zone, which netip counts as loopback too. Port 0 is for the system to choose.
func checkAdminAddr(addr string) error {
	err := fmt.Errorf("%q is not a loopback IP address and a port, such as 127.0.0.1:8081 or [::1]:8081", addr)
	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return err
	}
	ip, parseErr := netip.ParseAddr(host)
	if parseErr != nil || strings.HasPrefix(addr, "[") != ip.Is6() {
		return err
	}
	if ip.Is4() && ip.As4()[0] != 127 || !ip.Is4() && ip != netip.IPv6Loopback() {
		return err
	}
	if _, portErr := strconv.ParseUint(port, 10, 16); portErr != nil {
		return err
	}
	return nil
}

// listenOrigin returns the origin of the http server that listens on ap.
func listenOrigin(ap netip.AddrPort) string {
	return "http://" + net.JoinHostPort(ap.Addr().String(), strconv.Itoa(int(ap.Port())))
}

// parseProxy reads s, an IP address, which stands for itself, or a CIDR range, as a trusted proxy.
func parseProxy(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		a, addrErr := netip.ParseAddr(s)
		// A zone may be any text, also one with a slash.
		if addrErr != nil || strings.Contains(s, "/") {
			return netip.Prefix{}, errors.New("not an IP address or a CIDR range")
		}
		a = a.Unmap()
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if p.Bits() == 0 {
		return netip.Prefix{}, errors.New("covers every address")
	}
	return p, nil
}

// warnNoProxy reports whether to warn that the server has no trusted proxy: an https origin means a reverse proxy, which
// every client counts as without one. Not for a socket, whose peer is always trusted.
func warnNoProxy(network, origin string, proxies []netip.Prefix) bool {
	return network == "tcp" && len(proxies) == 0 && strings.HasPrefix(strings.ToLower(origin), "https://")
}

const noProxyWarning = "whiteroom serve admin: warning: the origin is https, but no trusted proxy is given: behind a " +
	"reverse proxy every client counts as the proxy; give its address with -trusted-proxy, or trustedProxies in the " +
	"config file\n"

// serveAdmin runs whiteroom serve admin with args, those after admin, until ctx is done.
func serveAdmin(ctx context.Context, args []string, e env) int {
	flags, f := newServeAdminFlags(e.stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		return usageError(flags, "too many arguments: give at most one SITE, after the flags")
	}
	if msg := f.rangeError(); msg != "" {
		return usageError(flags, "%s", msg)
	}
	cfg, err := loadConfig(f.config)
	if err != nil {
		printConfigError(e.stderr, flags.Name(), err)
		return 2
	}
	s, ok := f.resolve(flags, cfg, e.stderr)
	if !ok {
		return 2
	}

	logger := log.New(e.stderr, "", log.LstdFlags)
	p := errorPrinter{w: e.stderr, command: flags.Name(), site: s.site, theme: s.theme}
	a, err := openAdmin(e, s, logger)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	defer a.close()
	if warnNoProxy(s.network, a.origin, s.proxies) {
		printf(e.stderr, "%s", noProxyWarning)
	}
	listening := a.listener.Addr().String()
	if s.network == "unix" {
		listening = "socket " + s.addr
	}
	printf(e.stdout, "serving admin of %s at %s (listening on %s)\n", s.data, a.origin, listening)
	return reportRun(ctx, a, p)
}

// reportRun runs a with runAdmin until ctx is done, and returns the exit code: 0, or 1 with the error printed by p if the
// HTTP server or the Publisher has failed.
func reportRun(ctx context.Context, a *adminServer, p errorPrinter) int {
	if err := runAdmin(ctx, a); err != nil {
		p.printError(ctx, err)
		return 1
	}
	return 0
}

// adminServer is what serve admin has opened.
type adminServer struct {
	listener  net.Listener
	origin    string
	publisher *publish.Publisher
	http      *http.Server
	closers   []func() // in the order of opening
}

// close closes what openAdmin has opened, in the opposite order.
func (a *adminServer) close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
}

// openAdmin opens the site, the data directory, the listener, the accounts and the Publisher, and makes the servers from
// them, as the settings s say. If it fails it closes what it has opened. Run and Serve of what it returns are not called.
func openAdmin(e env, s adminSettings, logger *log.Logger) (*adminServer, error) {
	a := &adminServer{}
	if err := a.open(e, s, logger); err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

// open does what openAdmin says, and leaves what it has opened in closers.
func (a *adminServer) open(e env, s adminSettings, logger *log.Logger) error {
	schema, themeFS, closeSite, err := openAdminSite(s.site, s.theme)
	if err != nil {
		return err
	}
	a.closers = append(a.closers, closeSite)
	st, err := store.Open(s.data)
	if err != nil {
		return err
	}
	a.closers = append(a.closers, func() {
		if err := st.Close(); err != nil {
			logger.Printf("closing the data directory: %v", err)
		}
	})
	if s.network == "unix" {
		a.listener, err = listenSocket(s.addr)
	} else {
		a.listener, err = net.Listen("tcp", s.addr)
	}
	if err != nil {
		return err
	}
	// Closing a socket's listener removes its file.
	a.closers = append(a.closers, func() { _ = a.listener.Close() })
	a.origin = s.origin
	if tcp, ok := a.listener.Addr().(*net.TCPAddr); ok && a.origin == "" {
		a.origin = listenOrigin(tcp.AddrPort())
	}
	accounts, err := e.newAccounts(st)
	if err != nil {
		return err
	}
	a.publisher, err = publish.New(publish.Options{
		Store: st, Schema: schema, Theme: themeFS, Out: s.out, CacheDir: s.cache, Memory: s.memory << 20, Workers: s.workers,
		OnRecordError: func(action audit.Action, err error) {
			logger.Printf("the record of %s was not written: %v", action, err)
		},
	})
	if err != nil {
		return err
	}
	handler, err := admin.New(admin.Config{
		Origin: a.origin, TrustedProxies: s.proxies, Accounts: accounts, ErrorLog: logger, Publisher: a.publisher,
	})
	if err != nil {
		return err
	}
	a.http = admin.NewHTTPServer(handler, logger)
	return nil
}

// maxSocketPath is the most bytes of the path of a Unix domain socket: 107 on Linux, whose address has room for 108 with the
// NUL that ends it.
const maxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// listenSocket listens on the Unix domain socket file at path, which it makes with the mode 0660: the owner and the group
// connect to it, as a reverse proxy does. A socket file that nobody listens on, as a server that was killed leaves, is
// replaced; any other file there is left as it is, and is an error. Closing the listener removes the file.
func listenSocket(path string) (net.Listener, error) {
	// Linux takes it for an abstract socket, which has no file: the dial below would miss a file of that name.
	if strings.HasPrefix(path, "@") {
		return nil, fmt.Errorf("listen unix %s: a path that starts with @ is of an abstract socket, which has no file: "+
			"start the path of a file with ./", path)
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	case info.Mode()&os.ModeSocket == 0:
		return nil, fmt.Errorf("listen unix %s: the path exists and is not a socket, and is left as it is", path)
	default:
		conn, err := net.DialTimeout("unix", path, time.Second)
		switch {
		case err == nil:
			_ = conn.Close()
			return nil, fmt.Errorf("listen unix %s: another process listens on the socket, which is left as it is", path)
		case !errors.Is(err, syscall.ECONNREFUSED):
			return nil, fmt.Errorf("listen unix %s: cannot tell whether another process listens on the socket, which is left "+
				"as it is: %w", path, err)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		if len(path) > maxSocketPath {
			err = fmt.Errorf("%w (the path of a socket has at most %d bytes, and this one has %d)", err, maxSocketPath, len(path))
		}
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// openAdminSite opens the site directory site, of which the server reads only the schema file and the theme: the theme
// in the directory theme unless it is "". closeSite closes what it has opened.
func openAdminSite(site, theme string) (schema content.SiteFile, themeFS fs.FS, closeSite func(), err error) {
	d, err := sitedir.Open(site)
	if err != nil {
		return content.SiteFile{}, nil, nil, err
	}
	if schema, err = d.Schema(); err != nil {
		_ = d.Close()
		return content.SiteFile{}, nil, nil, err
	}
	if theme == "" {
		if themeFS, err = d.Theme(); err != nil {
			_ = d.Close()
			return content.SiteFile{}, nil, nil, err
		}
		return schema, themeFS, func() { _ = d.Close() }, nil
	}
	root, err := os.OpenRoot(theme)
	if err != nil {
		_ = d.Close()
		return content.SiteFile{}, nil, nil, err
	}
	return schema, root.FS(), func() { _ = root.Close(); _ = d.Close() }, nil
}

// runAdmin serves and builds until ctx is done, or one of the two fails, and then stops them in this order: the HTTP
// server, which lets the requests in progress end; then the Publisher, whose build that runs is canceled. It returns
// the first failure.
func runAdmin(ctx context.Context, a *adminServer) error {
	// Not ctx: the builds go on while the requests end, which may ask for one.
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRun()
	serving, running := make(chan error, 1), make(chan error, 1)
	go func() { serving <- a.http.Serve(a.listener) }()
	go func() { running <- a.publisher.Run(runCtx) }()

	var failure error
	servingEnded, runningEnded := false, false
	select {
	case err := <-serving:
		servingEnded, failure = true, err
	case err := <-running:
		// Run returns nil only once runCtx is done.
		runningEnded, failure = true, err
		if failure == nil {
			failure = errors.New("the publisher has stopped")
		}
	case <-ctx.Done():
	}
	if !servingEnded {
		shutdown, cancel := context.WithTimeout(context.Background(), adminShutdownWait)
		if err := a.http.Shutdown(shutdown); err != nil {
			_ = a.http.Close()
		}
		cancel()
		if err := <-serving; failure == nil && !errors.Is(err, http.ErrServerClosed) {
			failure = err
		}
	}
	cancelRun()
	if !runningEnded {
		if err := <-running; failure == nil {
			failure = err
		}
	}
	return failure
}
