package build

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// serverSites are the sites of the tests with the real servers, and the requests to check: the fixture with all the
// redirects of the tests of the server files, at the root of its host, under a path, and with prefixDefaultLanguage
// under a path in Japanese with a space (which TestServerEscaping has percent-encoded); a site in one language, which
// has one 404 page; and one under a path that nginx.conf and .htaccess cannot have, which then have only the directory
// index.
var serverSites = []struct {
	name  string
	site  func(t *testing.T) *builder // loaded, with its redirects resolved
	cases func(b *builder) []serverCase
}{
	{"root", fixtureSite("https://example.com", false), fixtureCases},
	{"path", fixtureSite("https://example.com/blog", false), fixtureCases},
	{"prefix", fixtureSite("https://example.com/ブ ログ", true), fixtureCases},
	{"one language", oneLanguageSite("/blog"), oneLanguageCases},
	{"path for neither", oneLanguageSite("/a$b"), indexCases},
}

// TestNginx serves releases of serverSites with nginx, which includes their nginx.conf, and checks the responses.
func TestNginx(t *testing.T) {
	bin, err := exec.LookPath("nginx")
	if err != nil {
		t.Skip("nginx is not installed")
	}
	for _, s := range serverSites {
		t.Run(s.name, func(t *testing.T) {
			b := s.site(t)
			dir, root := serverRelease(t, b)
			checkServer(t, "nginx", startNginx(t, bin, dir, root), b, s.cases(b))
		})
	}
}

// TestApache serves releases of serverSites with Apache, which reads their .htaccess, and checks the responses.
func TestApache(t *testing.T) {
	bin, err := exec.LookPath("httpd")
	if err != nil {
		t.Skip("httpd is not installed")
	}
	if os.Geteuid() == 0 {
		t.Skip("httpd would serve the files of the tests as another user")
	}
	serverRoot := httpdRoot(t, bin)
	for _, s := range serverSites {
		t.Run(s.name, func(t *testing.T) {
			b := s.site(t)
			_, root := serverRelease(t, b)
			checkServer(t, "apache", startApache(t, bin, serverRoot, root), b, s.cases(b))
		})
	}
}

// fixtureSite returns a function that loads the fixture at siteURL, with prefixDefaultLanguage if prefix, and with all
// the redirects of the tests of the server files.
func fixtureSite(siteURL string, prefix bool) func(t *testing.T) *builder {
	return func(t *testing.T) *builder {
		b := mustLoad(t, serverSource(t, siteURL, prefix, basicRedirects, escapingRedirects, leftOutRedirects))
		mustResolve(t, b)
		return b
	}
}

// oneLanguageSite returns a function that makes a site in Japanese alone at the path base of its host, as plan would
// make it: the front page, its 404 page and its feed, and no redirects.
func oneLanguageSite(base string) func(t *testing.T) *builder {
	return func(t *testing.T) *builder {
		return &builder{
			site:    &content.Site{URL: "https://example.com" + base, Languages: []string{"ja"}, DefaultLanguage: "ja"},
			base:    base,
			pages:   []*page{{kind: theme.KindNotFound, language: "ja", path: "/404.html", file: "404.html"}},
			outputs: []output{{path: "/", file: "index.html"}, {path: "/404.html", file: "404.html"}, {path: "/feed/", file: "feed/index.xml"}},
		}
	}
}

// serverRelease writes a release of b: in public/, a file for each output that has one, whose content is its path; and
// the server files. It returns the directory of the release, and the directory that the servers serve: public/, or,
// for a site under a path, a directory that has a link to public/ at that path.
func serverRelease(t *testing.T, b *builder) (dir, root string) {
	t.Helper()
	dir, rel := newPublic(t)
	for _, o := range b.outputs {
		if o.file != "" && o.file != ".htaccess" {
			if err := writeFile(rel, "public/"+o.file, []byte(o.path)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := b.writeServerFiles(rel); err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(dir, "public")
	if b.base == "" {
		return dir, root
	}
	base, err := url.PathUnescape(b.base)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "www", filepath.FromSlash(base))
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "www")
}

// nginxTestConf is the configuration of nginx for the tests, with the files that it writes, the address, the root and
// nginx.conf: one process in the foreground, which writes its files in the directory of the test.
const nginxTestConf = `daemon off;
master_process off;
worker_processes 1;
pid %[1]s;
error_log %[2]s;
events {
    worker_connections 64;
}
http {
    access_log off;
    client_body_temp_path %[3]s;
    proxy_temp_path %[4]s;
    fastcgi_temp_path %[5]s;
    uwsgi_temp_path %[6]s;
    scgi_temp_path %[7]s;
    types {
        text/html html;
        application/xml xml;
        text/css css;
    }
    server {
        listen %[8]s;
        root %[9]s;
        include %[10]s;
    }
}
`

// startNginx runs nginx at a free port, serving root with nginx.conf of dir, the directory of a release, until the end
// of the test, and returns its address.
func startNginx(t *testing.T, bin, dir, root string) string {
	t.Helper()
	tmp := t.TempDir()
	in := func(name string) string { return nginxString(filepath.Join(tmp, name)) }
	addr := freeAddr(t)
	conf := fmt.Sprintf(nginxTestConf, in("nginx.pid"), in("error.log"), in("client_body"), in("proxy"),
		in("fastcgi"), in("uwsgi"), in("scgi"), addr, nginxString(root), nginxString(filepath.Join(dir, "nginx.conf")))
	name := filepath.Join(tmp, "nginx.test.conf")
	if err := os.WriteFile(name, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(tmp, "error.log")
	runServer(t, exec.Command(bin, "-p", tmp, "-c", name, "-e", log), addr, log)
	return addr
}

// httpdModules are the modules that Apache needs for the tests, which nixpkgs builds as shared modules.
var httpdModules = []string{"mpm_event", "unixd", "authz_core", "dir", "mime", "rewrite"}

// httpdRoot returns the root of httpd that "httpd -V" reports, whose modules/ has httpdModules; the test is skipped if
// it does not.
func httpdRoot(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "-V").Output()
	if err != nil {
		t.Fatalf("httpd -V: %v", err)
	}
	m := regexp.MustCompile(`-D HTTPD_ROOT="([^"]*)"`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("httpd -V does not show HTTPD_ROOT:\n%s", out)
	}
	root := string(m[1])
	for _, name := range httpdModules {
		if _, err := os.Stat(filepath.Join(root, "modules", "mod_"+name+".so")); err != nil {
			t.Skipf("httpd has no shared module %s: %v", name, err)
		}
	}
	return root
}

// apacheTestConf is the configuration of Apache for the tests, with the root of httpd, the address, the modules, the
// files that it writes and the document root: AllowOverride FileInfo Indexes for the document root, and
// <Files ".ht*"> as in the default configuration of httpd.
const apacheTestConf = `ServerRoot %[1]s
Listen %[2]s
ServerName 127.0.0.1
%[3]sPidFile %[4]s
ErrorLog %[5]s
DefaultRuntimeDir %[6]s
TypesConfig conf/mime.types
DocumentRoot %[7]s
<Directory />
    AllowOverride None
    Require all denied
</Directory>
<Directory %[7]s>
    AllowOverride FileInfo Indexes
    Require all granted
</Directory>
<Files ".ht*">
    Require all denied
</Files>
`

// startApache runs Apache at a free port, serving root, until the end of the test, and returns its address.
func startApache(t *testing.T, bin, serverRoot, root string) string {
	t.Helper()
	tmp := t.TempDir()
	var modules strings.Builder
	for _, name := range httpdModules {
		fmt.Fprintf(&modules, "LoadModule %s_module modules/mod_%s.so\n", name, name)
	}
	addr := freeAddr(t)
	log := filepath.Join(tmp, "error.log")
	conf := fmt.Sprintf(apacheTestConf, nginxString(serverRoot), addr, modules.String(),
		nginxString(filepath.Join(tmp, "httpd.pid")), nginxString(log), nginxString(tmp), nginxString(root))
	name := filepath.Join(tmp, "httpd.conf")
	if err := os.WriteFile(name, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	// -X runs one process in the foreground, which leaves no children when it stops.
	runServer(t, exec.Command(bin, "-f", name, "-X"), addr, log)
	return addr
}

// freeAddr returns an address on the loopback interface with a port that nothing listens at. The tests that start
// servers run one after another: the system may give a port that has just been freed again, also to another test.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// runServer starts cmd, a server that listens at addr, and waits until it takes connections. At the end of the test, it
// stops the server and, if the test has failed, shows its output and the files of logs.
func runServer(t *testing.T, cmd *exec.Cmd, addr string, logs ...string) {
	t.Helper()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if cmd.Process.Signal(os.Interrupt) == nil {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
		_ = cmd.Process.Kill()
		<-done
		if t.Failed() {
			t.Logf("output of %s:\n%s", cmd, out.String())
			for _, name := range logs {
				data, err := os.ReadFile(name)
				t.Logf("%s (%v):\n%s", name, err, data)
			}
		}
	})
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		select {
		case <-done:
			t.Fatalf("%s has exited: %v", cmd, waitErr)
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("%s does not listen at %s: %v", cmd, addr, err)
		}
	}
}

// get sends a GET request for target, as it is, to addr, and returns the status, the Location and the body of the
// response.
func get(t *testing.T, addr, target string) (status int, location, body string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target, addr); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(data)
}

// serverCase is a request and the response that it must get.
type serverCase struct {
	target string // from the site URL, percent-encoded as it is sent
	from   string // that of the redirect that must apply; "" for none
	// Without from: the status, the Location (a path from the site URL, "" for none), and the path of the output that
	// the body must be ("" to not check it). With denied, the status may be 403 or 404.
	status   int
	location string
	body     string
	denied   bool
}

func redirectCase(target, from string) serverCase { return serverCase{target: target, from: from} }

func pageCase(target, path string) serverCase {
	return serverCase{target: target, status: http.StatusOK, body: path}
}

func notFoundCase(target, page string) serverCase {
	return serverCase{target: target, status: http.StatusNotFound, body: page}
}

// slashCase returns, for a site under a path, the request for that path without the trailing "/", which the servers
// redirect to the path with it, as they do for directories; the query stays, so that its redirect may apply after.
func slashCase(b *builder) []serverCase {
	if b.base == "" {
		return nil
	}
	return []serverCase{{target: "?p=123", status: http.StatusMovedPermanently, location: "/?p=123"}}
}

// fixtureCases are the requests to check for fixtureSite.
func fixtureCases(b *builder) []serverCase {
	ja := b.site.LanguagePath("ja")
	front := serverCase{status: http.StatusOK, body: "/"}
	if b.site.PrefixDefaultLanguage {
		front = serverCase{from: "/"}
	}
	atFront := func(target string) serverCase {
		c := front
		c.target = target
		return c
	}
	cases := []serverCase{
		redirectCase("/?p=123&utm_source=x", "/?p=123"),
		redirectCase("/?utm=x&p=123", "/?p=123"),
		atFront("/?P=123"),
		atFront("/?p=1234"),
		atFront("/?xp=123"),
		atFront("/"),
		// The redirect with more parameters comes first, though "/?a=1" is the smaller.
		redirectCase("/?b=2&a=1", "/?a=1&b=2"),
		redirectCase("/?a=1&utm=x&b=2", "/?a=1&b=2"),
		redirectCase("/?a=1", "/?a=1"),
		redirectCase("/?b=3&a=1", "/?a=1"),
		atFront("/?b=2"),
		redirectCase("/%E6%97%A7%E3%83%9A%E3%83%BC%E3%82%B8/", "/旧ページ/"),
		redirectCase("/%E6%97%A7%E3%83%9A%E3%83%BC%E3%82%B8", "/旧ページ/"),
		redirectCase("/%E6%97%A7%E3%83%9A%E3%83%BC%E3%82%B8/?x=1", "/旧ページ/"),
		redirectCase("/partner", "/partner/"),
		redirectCase("/partner/?a=1", "/partner/"),
		redirectCase("/news?r=1", "/news/?r=1"),
		redirectCase("/news/?x=0&r=1", "/news/?r=1"),
		redirectCase("/?q=%E6%96%B0", "/?q=新"),
		atFront("/?q=%e6%96%b0"),
		redirectCase("/?x=$y", "/?x=$y"),
		atFront("/?x=%24y"),
		redirectCase("/$var/", "/$var/"),
		redirectCase("/%24var", "/$var/"),
		redirectCase("/a%20b%22c%5Cn%3Fe%23f%25/", "/a%20b%22c%5Cn%3Fe%23f%25/"),
		redirectCase("/a%20b%22c%5Cn%3Fe%23f%25", "/a%20b%22c%5Cn%3Fe%23f%25/"),
		redirectCase("/?e", "/?e"),
		redirectCase("/?z=1&e", "/?e"),
		atFront("/?e=1"),
		redirectCase("/?k=a%2Bb", "/?k=a%2Bb"),
		atFront("/?k=a+b"),
		// Of two with as many parameters, the smaller from.
		redirectCase("/?k=a%2Bb&e", "/?e"),
		redirectCase("/old.html", "/old.html"),
		redirectCase("/old%2Ehtml/", "/old.html"),
		{target: "/oldxhtml", status: http.StatusNotFound},
		// nginx and Apache decode "/a+b/" and "/a%2Bb/" alike, and the latter comes first.
		redirectCase("/a+b/", "/a%2Bb/"),
		redirectCase("/a%2Bb", "/a%2Bb/"),
		{target: "/a%2Fb/", status: http.StatusNotFound},
		{target: "/a%01b/", status: http.StatusNotFound},
		{target: "/a//b/", status: http.StatusNotFound},
		{target: "/%FF/", status: http.StatusNotFound},
		{target: "/.htaccess?x=1", denied: true},
		{target: "/.htaccess", denied: true},
		{target: "/%2Ehtaccess", denied: true},
		pageCase(ja+"news/", ja+"news/"),
		{target: ja + "news", status: http.StatusMovedPermanently, location: ja + "news/"},
		{target: ja + "news?x=1", status: http.StatusMovedPermanently, location: ja + "news/?x=1"},
		pageCase(ja+"feed/", ja+"feed/"),
		pageCase("/en/feed/", "/en/feed/"),
		pageCase(ja+"2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/", ja+"2026/10/26/新サービス/"),
		pageCase("/en/", "/en/"),
		notFoundCase("/missing", ja+"404.html"),
		notFoundCase(ja+"missing/x/", ja+"404.html"),
		notFoundCase("/enx/missing", ja+"404.html"),
		notFoundCase("/x/en/missing", ja+"404.html"),
		notFoundCase("/en/missing", "/en/404.html"),
		notFoundCase("/en/a/b/c", "/en/404.html"),
	}
	if b.site.PrefixDefaultLanguage {
		cases = append(cases,
			pageCase(ja, ja),
			redirectCase("/ja/home/", "/ja/home/"),
			redirectCase("/ja/home", "/ja/home/"),
			notFoundCase("/home/", ja+"404.html"),
		)
	} else {
		cases = append(cases,
			redirectCase("/home/", "/home/"),
			redirectCase("/home", "/home/"),
			redirectCase("/home/?x=1", "/home/"),
		)
	}
	return append(cases, slashCase(b)...)
}

// oneLanguageCases are the requests to check for oneLanguageSite, whose 404 page is that of every path.
func oneLanguageCases(b *builder) []serverCase {
	return append([]serverCase{
		pageCase("/", "/"),
		pageCase("/feed/", "/feed/"),
		notFoundCase("/missing", "/404.html"),
		notFoundCase("/en/missing", "/404.html"),
		{target: "/.htaccess", denied: true},
	}, slashCase(b)...)
}

// indexCases are the requests to check for a site whose server files have only the directory index.
func indexCases(*builder) []serverCase {
	return []serverCase{
		pageCase("/", "/"),
		pageCase("/feed/", "/feed/"),
		{target: "/missing", status: http.StatusNotFound},
	}
}

// checkServer checks the responses of server, "nginx" or "apache", at addr, which serves the release of b, to cases.
func checkServer(t *testing.T, server, addr string, b *builder, cases []serverCase) {
	t.Helper()
	base := render.EncodeURL(b.base)
	decodedBase, err := url.PathUnescape(b.base)
	if err != nil {
		t.Fatal(err)
	}
	redirects := make(map[string]redirect)
	for _, r := range b.serverRedirects {
		redirects[r.from] = r
	}
	for _, c := range cases {
		status, location, body := get(t, addr, base+c.target)
		got := fmt.Sprintf("%d %q", status, siteLocation(location))
		var want string
		switch {
		case c.from != "":
			r, ok := redirects[c.from]
			if !ok {
				t.Fatalf("no redirect from %q", c.from)
			}
			loc := b.location(r)
			if server == "nginx" {
				loc = strings.ReplaceAll(loc, "$", "%24")
			}
			want = fmt.Sprintf("%d %q", r.status, siteLocation(loc))
		case c.denied:
			if status == http.StatusForbidden {
				got = fmt.Sprintf("%d %q", http.StatusNotFound, siteLocation(location))
			}
			want = fmt.Sprintf("%d %q", http.StatusNotFound, "")
		default:
			want = fmt.Sprintf("%d %q", c.status, c.location)
			// A redirect of the server itself, whose Location Apache encodes in lower case.
			if decoded, err := url.PathUnescape(siteLocation(location)); err == nil && c.location != "" {
				got = fmt.Sprintf("%d %q", status, strings.TrimPrefix(decoded, decodedBase))
			}
		}
		if got != want {
			t.Errorf("%s: GET %s = %s, want %s", server, base+c.target, got, want)
		}
		if c.from == "" && c.body != "" && body != c.body {
			t.Errorf("%s: GET %s has the body %q, want %q", server, base+c.target, body, c.body)
		}
	}
}

// siteLocation returns loc, a Location, without the scheme and the host if it is in the site of the tests at
// 127.0.0.1, which the servers put before paths.
func siteLocation(loc string) string {
	if rest, ok := strings.CutPrefix(loc, "http://127.0.0.1:"); ok {
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			return rest[i:]
		}
	}
	return loc
}
