package admin

import (
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// sampleSchemaJSON is GET /api/schema for the sample site, whose schema file (internal/build/testdata/site/site/schema.yaml)
// sets the URL rule of post, and adds the type works with fields and the taxonomy works_category. It is written from that
// file: post and page first, then works; category and tag first, then works_category.
const sampleSchemaJSON = `{"authorUrl":"/author/{slug}/","types":[` +
	`{"name":"post","url":"/{yyyy}/{mm}/{dd}/{slug}/","body":true,"taxonomies":["category","tag"],"fields":[]},` +
	`{"name":"page","url":"/{path}/","body":true,"taxonomies":[],"fields":[]},` +
	`{"name":"works","label":"制作実績","url":"/works/{slug}/","archive":"/works/","body":true,"taxonomies":["works_category"],"fields":[` +
	`{"name":"client","type":"text","label":"クライアント名","required":true,"translatable":true,"maxLength":100},` +
	`{"name":"year","type":"number","label":"制作年","translatable":false,"min":1990,"max":2100},` +
	`{"name":"images","type":"list","label":"画像","translatable":true,"maxItems":20,"of":{"type":"media","translatable":true,"accept":["image"]}}]}],` +
	`"taxonomies":[` +
	`{"name":"category","url":"/category/{path}/","hierarchical":true},` +
	`{"name":"tag","url":"/tag/{slug}/","hierarchical":false},` +
	`{"name":"works_category","label":"実績の種類","url":"/works_category/{slug}/","hierarchical":false}]}`

// sampleThemeJSON is GET /api/theme for the example theme (internal/theme/testdata/example/theme.yaml), written from that
// file: the blocks without styles of their own have those of the theme.
const sampleThemeJSON = `{"defaultLanguage":"ja","presets":{` +
	`"color":[{"name":"primary","value":"#1e73be"},{"name":"accent","value":"#e85d04"},{"name":"muted","value":"#f5f5f5"},{"name":"text","value":"oklch(25% 0 0)"}],` +
	`"fontSize":[{"name":"small","value":"0.875rem"},{"name":"medium","value":"1rem"},{"name":"large","value":"clamp(1.25rem, 1rem + 1vw, 1.5rem)"}],` +
	`"spacing":[{"name":"10","value":"0.25rem"},{"name":"20","value":"0.5rem"},{"name":"30","value":"1rem"},{"name":"40","value":"2rem"}],` +
	`"fontFamily":[{"name":"body","value":"\"Noto Sans JP\", sans-serif"},{"name":"mono","value":"ui-monospace, monospace"}],` +
	`"gradient":[{"name":"sunset","value":"linear-gradient(135deg, #f093fb 0%, #f5576c 100%)"}],` +
	`"shadow":[{"name":"soft","value":"0 2px 4px rgba(0, 0, 0, 0.1)"}]},` +
	`"styles":["textColor","backgroundColor","linkColor","fontSize","fontWeight","lineHeight","padding","margin","blockGap","border"],` +
	`"custom":{"color":false,"fontSize":true,"spacing":true},` +
	`"blocks":[` +
	`{"type":"paragraph","styles":["textColor","backgroundColor","linkColor","fontSize","fontWeight","lineHeight","padding","margin","blockGap","border"],"variants":[{"name":"lead","label":"リード文"}]},` +
	`{"type":"heading","styles":["textColor","gradient","fontSize","fontFamily","letterSpacing","margin"],"variants":[]},` +
	`{"type":"code","styles":["fontSize","backgroundColor"],"variants":[]},` +
	`{"type":"group","styles":["textColor","backgroundColor","linkColor","fontSize","fontWeight","lineHeight","padding","margin","blockGap","border"],"variants":[{"name":"box","label":"ボックス"},{"name":"note","label":"注意書き"}]}],` +
	`"marks":{"highlight":{"variants":[{"name":"marker","label":"マーカー"}]}},` +
	`"locations":[{"name":"header","label":"ヘッダー"},{"name":"footer","label":"フッター"}],` +
	`"pageTemplates":[{"name":"landing","label":"ランディングページ"}],` +
	`"imageSizes":[{"name":"thumbnail","label":"サムネイル","width":150,"height":150,"crop":true},` +
	`{"name":"medium","label":"中","width":300,"crop":false},{"name":"large","label":"大","width":1024,"crop":false}],` +
	`"mobileWidth":"600px"}`

func TestDefinitions(t *testing.T) {
	a := newAPIServer(t)
	var reached int
	a.hook = func(point string) {
		if point == "checked" {
			reached++
		}
	}
	for _, tt := range []struct{ path, want string }{
		{"/api/schema", sampleSchemaJSON},
		{"/api/theme", sampleThemeJSON},
	} {
		// Every role may read them.
		for _, l := range apiLogins {
			res := a.as(l.login).get(tt.path)
			if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != tt.want {
				t.Errorf("GET %s as %s: %d\n%s\nwant 200\n%s", tt.path, l.login, res.StatusCode, got, tt.want)
			}
			if got := res.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("GET %s has the Content-Type %q", tt.path, got)
			}
			checkHeaders(t, "GET "+tt.path, res, true)
		}
		if reached != len(apiLogins) {
			t.Errorf("GET %s: the hook \"checked\" was called %d times for %d requests", tt.path, reached, len(apiLogins))
		}
		reached = 0
		if tt.path == "/api/schema" {
			schema := decodeAnswer[schemaAnswer](t, a.as("editor").get(tt.path))
			if len(schema.Types) != 3 || schema.Types[2].Name != "works" || len(schema.Types[2].Fields) != 3 || len(schema.Taxonomies) != 3 {
				t.Errorf("the schema reads as %+v, want 3 types, 3 fields of works and 3 taxonomies", schema)
			}
			reached = 0
		}

		// Without a session, or from a page of another site, the route is not reached.
		res := a.send(http.MethodGet, tt.path, "")
		checkProblem(t, "GET "+tt.path+" without a login", res, "unauthenticated")
		res = a.send(http.MethodGet, tt.path, "", append(a.cookie(a.people["admin"].token), "Sec-Fetch-Site", "cross-site")...)
		checkProblem(t, "GET "+tt.path+" from another site", res, "cross-origin")
		res = a.send(http.MethodGet, tt.path, "", append(a.cookie("no-such-token-no-such-token-no-such-token-"), "Sec-Fetch-Site", "same-origin")...)
		checkProblem(t, "GET "+tt.path+" with a token that is no session", res, "unauthenticated")
		if reached != 0 {
			t.Errorf("GET %s: the route was reached %d times by requests that must not reach it", tt.path, reached)
		}

		// Only GET and HEAD.
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if method == http.MethodPost {
				res = a.as("admin").post(tt.path, "")
			} else {
				res = a.as("admin").send(method, tt.path, "")
			}
			if got := res.Header.Get("Allow"); got != "GET, HEAD" {
				t.Errorf("%s %s has the Allow %q, want GET, HEAD", method, tt.path, got)
			}
			checkProblem(t, method+" "+tt.path, res, "method-not-allowed")
		}
	}
	if reached != 0 {
		t.Errorf("the hook \"checked\" was called %d times by requests that must not reach the route", reached)
	}
	if got := a.records(t, audit.AccessDenied); len(got) != 0 {
		t.Errorf("reading the definitions made %d records of access.denied, want none", len(got))
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// definitionJSON returns the JSON that GET /api/schema answers for the schema file schema.
func definitionJSON(t *testing.T, schema string) string {
	t.Helper()
	site := new(content.Site)
	if err := site.UnmarshalSchema([]byte(schema)); err != nil {
		t.Fatalf("UnmarshalSchema() error = %v", err)
	}
	b, err := json.Marshal(schemaOf(site))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSchemaOf shows the parts of a schema that the sample does not have: a group, the options of a select, an item of a
// list, a minimum of 0, and the URL rules and the labels that the schema file gives to the built-in types and taxonomies.
func TestSchemaOf(t *testing.T) {
	const schema = `schemaVersion: 1
authorUrl: /by/{slug}/
taxonomies:
  topic:
    label: Topics
    url: /topics/{path}/
    hierarchical: true
  tag:
    label: Labels
    url: /labels/{slug}/
types:
  page:
    label: Pages
    url: /{path}/
    fields:
      subtitle:
        type: text
  event:
    taxonomies: [topic]
    archive: /events/
    fields:
      place:
        type: group
        label: Place
        fields:
          name: { type: text, required: true, minLength: 1 }
          kind:
            type: select
            options: [hall, park]
            multiple: true
            minItems: 1
            maxItems: 2
      links:
        type: list
        of: { type: link }
      related:
        type: entry
        types: [post]
        multiple: true
      theme:
        type: term
        taxonomy: topic
      price:
        type: number
        min: 0
        required: false
`
	const want = `{"authorUrl":"/by/{slug}/","types":[` +
		`{"name":"post","url":"/{yyyy}/{mm}/{dd}/{slug}/","body":true,"taxonomies":["category","tag"],"fields":[]},` +
		`{"name":"page","label":"Pages","url":"/{path}/","body":true,"taxonomies":[],"fields":[` +
		`{"name":"subtitle","type":"text","translatable":true}]},` +
		`{"name":"event","url":"/event/{slug}/","archive":"/events/","body":false,"taxonomies":["topic"],"fields":[` +
		`{"name":"place","type":"group","label":"Place","translatable":true,"fields":[` +
		`{"name":"name","type":"text","required":true,"translatable":true,"minLength":1},` +
		`{"name":"kind","type":"select","translatable":true,"options":["hall","park"],"multiple":true,"minItems":1,"maxItems":2}]},` +
		`{"name":"links","type":"list","translatable":true,"of":{"type":"link","translatable":true}},` +
		`{"name":"related","type":"entry","translatable":true,"multiple":true,"types":["post"]},` +
		`{"name":"theme","type":"term","translatable":true,"taxonomy":"topic"},` +
		`{"name":"price","type":"number","translatable":true,"min":0}]}],` +
		`"taxonomies":[` +
		`{"name":"category","url":"/category/{path}/","hierarchical":true},` +
		`{"name":"tag","label":"Labels","url":"/labels/{slug}/","hierarchical":false},` +
		`{"name":"topic","label":"Topics","url":"/topics/{path}/","hierarchical":true}]}`
	if got := definitionJSON(t, schema); got != want {
		t.Errorf("the definitions of the schema are\n%s\nwant\n%s", got, want)
	}

	// A schema file without types and taxonomies has the built-in ones.
	const empty = `{"authorUrl":"/author/{slug}/","types":[` +
		`{"name":"post","url":"/{yyyy}/{mm}/{dd}/{slug}/","body":true,"taxonomies":["category","tag"],"fields":[]},` +
		`{"name":"page","url":"/{path}/","body":true,"taxonomies":[],"fields":[]}],"taxonomies":[` +
		`{"name":"category","url":"/category/{path}/","hierarchical":true},{"name":"tag","url":"/tag/{slug}/","hierarchical":false}]}`
	if got := definitionJSON(t, "schemaVersion: 1\n"); got != empty {
		t.Errorf("the definitions of an empty schema are\n%s\nwant\n%s", got, empty)
	}
}

// TestThemeOf shows what a theme leaves out: no preset, every style item, every custom value, the default width.
func TestThemeOf(t *testing.T) {
	settings, err := content.LoadThemeSettings("theme.yaml", []byte("schemaVersion: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(themeOf(settings))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"defaultLanguage":"","presets":{"color":[],"fontSize":[],"spacing":[],"fontFamily":[],"gradient":[],"shadow":[]},` +
		`"styles":["textColor","backgroundColor","linkColor","gradient","fontSize","letterSpacing","fontFamily","fontWeight","lineHeight","padding","margin","blockGap","border"],` +
		`"custom":{"color":true,"fontSize":true,"spacing":true},"blocks":[],"marks":{"highlight":{"variants":[]}},"locations":[],"pageTemplates":[],` +
		`"imageSizes":[],"mobileWidth":"781px"}`
	if string(b) != want {
		t.Errorf("the theme without settings is\n%s\nwant\n%s", b, want)
	}

	const themeFile = `schemaVersion: 1
custom:
  fontSize: false
  spacing: false
blocks:
  heading:
    styles: [textColor]
  quote:
    variants:
      big: Big
imageSizes:
  wide: { label: Wide, height: 200 }
`
	settings, err = content.LoadThemeSettings("theme.yaml", []byte(themeFile))
	if err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(themeOf(settings))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`"custom":{"color":true,"fontSize":false,"spacing":false}`,
		`"blocks":[{"type":"heading","styles":["textColor"],"variants":[]},{"type":"quote","styles":["textColor","backgroundColor",`,
		`"variants":[{"name":"big","label":"Big"}]}]`,
		`"imageSizes":[{"name":"wide","label":"Wide","height":200,"crop":false}]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the theme is\n%s\nwant it to have\n%s", got, want)
		}
	}
}

// TestDefinitionsWithoutPublisher shows that a server without a Publisher has no routes for the definitions.
func TestDefinitionsWithoutPublisher(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	token := ts.logIn(t, "admin")
	for _, path := range []string{"/api/schema", "/api/theme"} {
		checkProblem(t, "GET "+path+" without a Publisher", ts.send(http.MethodGet, path, "", ts.cookie(token)...), "not-found")
		checkProblem(t, "GET "+path+" without a Publisher and a login", ts.send(http.MethodGet, path, ""), "not-found")
	}
}

// TestAPIServer shows the rig: the Publisher builds the sample site at its start, and the accounts are logged in as
// themselves.
func TestAPIServer(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	status := a.pub.Status()
	if status.Last == nil || status.Last.Err != nil || status.Last.Result == nil || status.Running != nil || status.Pending {
		t.Fatalf("the Publisher has not built the sample site: %+v", status)
	}
	rel, err := a.pub.Releases()
	if err != nil || rel.Current == "" || len(rel.Names) != 1 {
		t.Errorf("Releases() = %+v, %v, want one release that is the current", rel, err)
	}
	for _, l := range apiLogins {
		p := a.principal(t, l.login)
		if p.Account.Role != l.role || p.Account.Login != l.login || p.Account.ID != a.people[l.login].account.ID {
			t.Errorf("the session of %s is that of %+v", l.login, p.Account)
		}
	}
}
