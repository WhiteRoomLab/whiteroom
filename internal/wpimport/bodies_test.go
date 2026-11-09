package wpimport

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/phpserialize"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// bodiesOut holds the golden files of TestBodies, which go test -update writes: <post ID>.json, what Body made of the
// content of the post (the number of the blocks before the read more block, and the blocks); <post ID>-allowhtml.json, the
// same for the posts with custom HTML if the site allows it; report.txt and report-allowhtml.txt, the reports of the two
// imports; and render.txt, the warnings of the renderer of the example theme.
const bodiesOut = "testdata/out-blocks"

func readBodies(t testing.TB) (*wxr.Export, *Report) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "blocks.xml"))
	if err != nil {
		t.Fatal(err)
	}
	e, rep, err := Read(context.Background(), "blocks.xml", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return e, rep
}

// bodyPosts returns the posts and the pages of e that have a content.
func bodyPosts(e *wxr.Export) []*wxr.Item {
	var posts []*wxr.Item
	for i := range e.Items {
		if it := &e.Items[i]; (it.Type == "post" || it.Type == "page") && !blank(it.Content) {
			posts = append(posts, it)
		}
	}
	return posts
}

func hasCustomHTML(it *wxr.Item) bool { return strings.Contains(it.Content, "<!-- wp:html") }

func attachedFile(it *wxr.Item) string {
	file, _ := it.Meta.Get("_wp_attached_file")
	return file
}

// newBodiesImport is the import of e in which the plan has given every post, page, attachment and form its ID, and the
// conversions of the entries, the media and the forms have recorded them, as the real ones will.
func newBodiesImport(e *wxr.Export, rep *Report) (*Import, error) {
	imp := New("blocks.xml", e, rep)
	for i := range e.Items {
		it := &e.Items[i]
		var kind content.RefKind
		var ref content.Ref
		switch it.Type {
		case "post":
			kind, ref = content.RefEntry, content.Ref{Type: content.TypePost, Language: "ja"}
		case "page":
			kind, ref = content.RefEntry, content.Ref{Type: content.TypePage, Language: "ja"}
		case "attachment":
			kind, ref = content.RefMedia, content.Ref{Type: content.MediaImage}
			if path.Ext(attachedFile(it)) == ".pdf" {
				ref.Type = content.MediaPDF
			}
		case formType:
			kind = content.RefForm
		default:
			continue
		}
		id, err := imp.IDs.Assign(PostObject(it), kind)
		if err != nil {
			return nil, err
		}
		imp.AddRef(kind, id, ref)
	}
	return imp, nil
}

// bodiesSettings are the settings after the person who imports has corrected them: how the presets, the block styles, the
// classes of the marks and the image sizes of WordPress are named in the example theme.
func bodiesSettings(allowHTML bool) *Settings {
	s := &Settings{
		Site: SiteSettings{
			Name: "ブロックの見本サイト", Tagline: "手で書いた、ブロックエディターの本文の見本です", URL: "https://example.com",
			Language: "ja", TimeZone: "Asia/Tokyo", PostsPerPage: 10, AllowHTML: allowHTML,
		},
		Blocks: emptyBlockSettings(),
	}
	b := &s.Blocks
	// A preset that is not a key keeps its name, so black and large are the presets of the same names in the theme.
	b.Presets.Color["vivid-red"], b.Presets.Color["pale-pink"] = "accent", ""
	b.Presets.Gradient["vivid-cyan-blue-to-vivid-purple"] = "sunset"
	b.Presets.FontSize["x-large"] = "large"
	b.Presets.FontFamily["system-font"] = "body"
	b.Presets.Spacing["50"], b.Presets.Spacing["60"] = "40", ""
	b.Variants["lead-text"], b.Variants["boxed"], b.Variants["outline"] = "lead", "box", ""
	b.Marks["marker-yellow"] = "marker"
	b.ImageSizes["medium_large"] = ""
	return s
}

// bodiesResolver is the render.Resolver of the bodies: the titles and the paths of the entries, the media and their files,
// and the form.
type bodiesResolver struct {
	entries map[string][2]string
	media   map[string]*content.Media
	forms   map[string]*content.Form
}

func (r *bodiesResolver) Entry(id string) (title, sitePath string, ok bool) {
	e, ok := r.entries[id]
	return e[0], e[1], ok
}

func mediaPath(m *content.Media) string { return "/media/" + m.ID + "/" + m.FileName }

func (r *bodiesResolver) Media(id string) (*content.Media, string, bool) {
	m, ok := r.media[id]
	if !ok {
		return nil, "", false
	}
	return m, mediaPath(m), true
}

func (r *bodiesResolver) Image(id string, _ *content.ImageSize) (render.ImageFiles, bool) {
	m, ok := r.media[id]
	if !ok || m.Width <= 0 || m.Height <= 0 {
		return render.ImageFiles{}, false
	}
	return render.ImageFiles{Src: mediaPath(m), Width: m.Width, Height: m.Height}, true
}

func (r *bodiesResolver) Form(id string) (*content.Form, bool) {
	f, ok := r.forms[id]
	return f, ok
}

func newBodiesResolver(t testing.TB, imp *Import) *bodiesResolver {
	t.Helper()
	r := &bodiesResolver{
		entries: make(map[string][2]string), media: make(map[string]*content.Media), forms: make(map[string]*content.Form),
	}
	var form content.Form
	data, err := os.ReadFile(filepath.Join("..", "content", "testdata", "contact-form.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := form.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	// The pages that the consent field of the form refers to, which are not in the WXR.
	for _, field := range form.Fields {
		for _, id := range field.Policy {
			r.entries[id] = [2]string{"プライバシーポリシー", "/privacy/"}
		}
	}
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		kind, id, ok := imp.IDs.Lookup(PostObject(it))
		if !ok {
			continue
		}
		switch kind {
		case content.RefEntry:
			u, err := url.Parse(it.Link)
			if err != nil {
				t.Fatal(err)
			}
			r.entries[id] = [2]string{it.Title, u.Path}
		case content.RefMedia:
			file := path.Base(attachedFile(it))
			m := &content.Media{ID: id, FileName: file}
			switch path.Ext(file) {
			case ".jpg":
				m.MIMEType = "image/jpeg"
			case ".png":
				m.MIMEType = "image/png"
			case ".pdf":
				m.MIMEType = "application/pdf"
			}
			if meta, ok := it.Meta.Get("_wp_attachment_metadata"); ok {
				v, err := Unserialize(nil, Source{}, wxr.Meta{Key: "_wp_attachment_metadata", Value: meta})
				if err != nil {
					t.Fatal(err)
				}
				size := func(key string) int {
					n, _ := v.Lookup(key)
					if n.Kind != phpserialize.Int {
						t.Fatalf("%s of the attachment %d is not a number", key, it.ID)
					}
					return int(n.Int)
				}
				m.Width, m.Height = size("width"), size("height")
			}
			if alt, ok := it.Meta.Get("_wp_attachment_image_alt"); ok {
				m.Alt = content.Localized{"ja": alt}
			}
			r.media[id] = m
		case content.RefForm:
			r.forms[id] = &form
		case content.RefTerm, content.RefAuthor, content.RefMenu, content.RefRedirect, content.RefRevision,
			content.RefTranslationGroup, content.RefUser:
		}
	}
	return r
}

type bodiesResult struct {
	item   *wxr.Item
	target Target
	res    *BodyResult
}

// convertBodies converts the content of the posts of imp that keep says yes to, as the conversion of the entries will: the
// key is the ID of the revision that the entry gets, and the target is revisions/<ID>.json#/body.
func convertBodies(t *testing.T, imp *Import, allowHTML bool, keep func(*wxr.Item) bool) []bodiesResult {
	t.Helper()
	settings := bodiesSettings(allowHTML)
	site, err := imp.Site(settings, nil)
	if err != nil {
		t.Fatalf("Site = %v", err)
	}
	var results []bodiesResult
	for _, it := range bodyPosts(imp.WXR) {
		if !keep(it) {
			continue
		}
		revision := imp.IDs.NewID(content.RefRevision, PostObject(it).key())
		target := Target{File: "revisions/" + revision + ".json", Pointer: "/body"}
		res, err := imp.Body(BodyOptions{Site: site, Settings: settings, Item: it, Target: target, Key: revision})
		if err != nil {
			t.Fatalf("Body of the post %d = %v", it.ID, err)
		}
		checkBody(t, imp, site, res)
		checkRevision(t, imp, site, it, res)
		results = append(results, bodiesResult{it, target, res})
	}
	noGuard(t, imp)
	return results
}

// checkRevision checks that the blocks pass as the body of a draft revision of the entry of the post.
func checkRevision(t testing.TB, imp *Import, site *content.Site, it *wxr.Item, res *BodyResult) {
	t.Helper()
	_, entry, ok := imp.IDs.Lookup(PostObject(it))
	if !ok {
		t.Fatalf("the post %d has no ID", it.ID)
	}
	typ := content.TypePost
	if it.Type == "page" {
		typ = content.TypePage
	}
	err := content.Revision{Body: res.Blocks}.Validate(content.RevisionOptions{
		Site: site, Entry: &content.Entry{ID: entry, Type: typ, Language: "ja"}, Import: true, Lookup: imp.Ref,
	})
	if err != nil {
		t.Errorf("the body of the post %d does not pass Validate as a revision: %v", it.ID, err)
	}
}

// bodyDocument writes the golden file of a result: More and the blocks as JSON, with two spaces to indent.
func bodyDocument(t testing.TB, res *BodyResult) string {
	t.Helper()
	b, err := json.Marshal(struct {
		More int            `json:"more"`
		Body content.Blocks `json:"body"`
	}{res.More, res.Blocks}, jsontext.WithIndent("  "))
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// checkTargets checks that every entry of the report points into the blocks of the result of its file.
func checkTargets(t testing.TB, rep *Report, results []bodiesResult) {
	t.Helper()
	byFile := make(map[string]bodiesResult)
	for _, r := range results {
		byFile[r.target.File] = r
	}
	for _, e := range rep.Entries() {
		r, ok := byFile[e.Target.File]
		if !ok {
			if e.Target.File != "" {
				t.Errorf("the entry %q is about the file %s, which is none of the bodies", e.Message, e.Target.File)
			}
			continue
		}
		rest, ok := strings.CutPrefix(e.Target.Pointer, r.target.Pointer)
		if !ok || !pointsIntoBlocks(r.res.Blocks, rest) {
			t.Errorf("the entry %q is about %s#%s, which is not in the blocks of the body", e.Message, e.Target.File, e.Target.Pointer)
		}
	}
}

// pointsIntoBlocks reports whether the pointer, from the body, is the body, or goes down /<index>/content/<index>... to a
// block, and then ends, or goes on with /attrs, or ends with /content (the text of the block, or its blocks if it has none).
func pointsIntoBlocks(blocks content.Blocks, pointer string) bool {
	if pointer == "" {
		return true
	}
	segs := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	i, err := strconv.Atoi(segs[0])
	if err != nil || i < 0 || i >= len(blocks) {
		return false
	}
	cur, rest := &blocks[i], segs[1:]
	for len(rest) >= 2 && rest[0] == "content" {
		j, err := strconv.Atoi(rest[1])
		if err != nil || j < 0 || j >= len(cur.Children) {
			return false
		}
		cur, rest = &cur.Children[j], rest[2:]
	}
	return len(rest) == 0 || rest[0] == "attrs" || len(rest) == 1 && rest[0] == "content"
}

func TestPointsIntoBlocks(t *testing.T) {
	table := content.Block{Type: content.BlockTable, Children: content.Blocks{
		{Type: content.BlockTableRow, Children: content.Blocks{{Type: content.BlockTableCell, Text: content.RichText{{Type: content.InlineText, Text: "a"}}}}},
	}}
	blocks := content.Blocks{
		{Type: content.BlockImage},
		{Type: content.BlockGroup, Children: content.Blocks{{Type: content.BlockParagraph, Text: content.RichText{{Type: content.InlineText, Text: "b"}}}, table}},
	}
	for pointer, want := range map[string]bool{
		"": true, "/0": true, "/1": true, "/0/attrs/alt": true, "/1/content": true, "/1/content/0": true, "/1/content/0/content": true,
		"/1/content/1": true, "/1/content/1/content/0": true, "/1/content/1/content/0/content/0": true,
		"/1/content/1/content/0/content/0/content": true,
		"/2": false, "/-1": false, "/x": false, "/0/content/0": false, "/1/content/2": false, "/1/content/0/content/0": false,
		"/1/content/1/content/1": false, "/1/content/1/content/0/content/0/content/0": false, "/1/text": false,
	} {
		if got := pointsIntoBlocks(blocks, pointer); got != want {
			t.Errorf("pointsIntoBlocks(%q) = %v, want %v", pointer, got, want)
		}
	}
}

// bodyFeatures adds to into what the blocks use, as "block:paragraph", "mark:bold", "link:entry", "prop:style",
// "layout:row" and the like.
func bodyFeatures(blocks content.Blocks, into map[string]bool) {
	link := func(l *content.Link) {
		switch {
		case l == nil:
		case l.Entry != "":
			into["link:entry"] = true
		case l.Media != "":
			into["link:media"] = true
		case strings.HasPrefix(l.Href, "/"):
			into["link:path"] = true
		case l.Href != "":
			into["link:external"] = true
		}
	}
	texts := func(rt content.RichText) {
		for _, in := range rt {
			into["inline:"+string(in.Type)] = true
			for _, m := range in.Marks {
				into["mark:"+string(m.Type)] = true
				if m.Type == content.MarkLink {
					link(m.Link)
				}
			}
		}
	}
	var walk func(bs content.Blocks)
	walk = func(bs content.Blocks) {
		for i := range bs {
			b := &bs[i]
			into["block:"+string(b.Type)] = true
			for name, on := range map[string]bool{
				"anchor": b.Anchor != "", "className": b.ClassName != "", "variant": b.Variant != "",
				"style": !reflect.ValueOf(b.Style).IsZero(),
			} {
				if on {
					into["prop:"+name] = true
				}
			}
			if b.Attrs.Layout.Type != "" {
				into["layout:"+b.Attrs.Layout.Type] = true
			}
			link(b.Attrs.Link)
			texts(b.Text)
			texts(b.Attrs.Caption)
			texts(b.Attrs.Citation)
			walk(b.Children)
		}
	}
	walk(blocks)
}

// TestBodies converts the content of the posts of testdata/blocks.xml, which has all that the table of 6.10.2 has, and
// compares the blocks and the report with the golden files in testdata/out-blocks, which go test -update rewrites. The
// blocks pass as the body of a draft revision, and the example theme renders them.
func TestBodies(t *testing.T) {
	e, rep := readBodies(t)
	imp, err := newBodiesImport(e, rep)
	if err != nil {
		t.Fatal(err)
	}
	results := convertBodies(t, imp, false, func(*wxr.Item) bool { return true })
	if len(results) != len(bodyPosts(e)) {
		t.Fatalf("%d bodies were converted, want %d", len(results), len(bodyPosts(e)))
	}
	checkTargets(t, imp.Report, results)

	golden := make(map[string]string)
	golden["report.txt"] = reportText(t, imp.Report)

	th, err := theme.Load(os.DirFS("../theme/testdata/example"))
	if err != nil {
		t.Fatalf("theme.Load = %v", err)
	}
	tr, err := th.Renderer("ja", "")
	if err != nil {
		t.Fatal(err)
	}
	renderer := render.New(tr, newBodiesResolver(t, imp))
	var warnings strings.Builder
	features := make(map[string]bool)
	for _, r := range results {
		golden[strconv.FormatInt(r.item.ID, 10)+".json"] = bodyDocument(t, r.res)
		bodyFeatures(r.res.Blocks, features)
		_, _, ws, err := renderer.Document(r.res.Blocks)
		if err != nil {
			t.Errorf("Document of the body of the post %d = %v", r.item.ID, err)
		}
		for _, w := range ws {
			fmt.Fprintf(&warnings, "%d: %s\n", r.item.ID, w)
		}
	}
	golden["render.txt"] = warnings.String()

	// The same with a site that allows custom HTML, for the posts that have it: the blocks that were legacy blocks are html
	// blocks.
	e, rep = readBodies(t)
	other, err := newBodiesImport(e, rep)
	if err != nil {
		t.Fatal(err)
	}
	withHTML := convertBodies(t, other, true, hasCustomHTML)
	if len(withHTML) == 0 {
		t.Fatal("no post has custom HTML")
	}
	checkTargets(t, other.Report, withHTML)
	golden["report-allowhtml.txt"] = reportText(t, other.Report)
	renderer = render.New(tr, newBodiesResolver(t, other))
	for _, r := range withHTML {
		golden[strconv.FormatInt(r.item.ID, 10)+"-allowhtml.json"] = bodyDocument(t, r.res)
		bodyFeatures(r.res.Blocks, features)
		if _, _, _, err := renderer.Document(r.res.Blocks); err != nil {
			t.Errorf("Document of the body of the post %d with custom HTML = %v", r.item.ID, err)
		}
	}

	// The posts have all that the table has: if one of these goes, the posts no longer show it.
	for _, want := range []string{
		"block:paragraph", "block:heading", "block:list", "block:listItem", "block:quote", "block:table", "block:tableRow",
		"block:tableCell", "block:image", "block:buttons", "block:button", "block:separator", "block:spacer", "block:code",
		"block:embed", "block:columns", "block:column", "block:group", "block:file", "block:form", "block:html", "block:legacy",
		"inline:text", "inline:hardBreak", "mark:bold", "mark:italic", "mark:underline", "mark:strike", "mark:code",
		"mark:subscript", "mark:superscript", "mark:highlight", "mark:link", "link:entry", "link:media", "link:path",
		"link:external", "prop:anchor", "prop:className", "prop:variant", "prop:style", "layout:row", "layout:stack",
		"layout:grid",
	} {
		if !features[want] {
			t.Errorf("no body has %s", want)
		}
	}

	if *update {
		if err := os.RemoveAll(bodiesOut); err != nil {
			t.Fatal(err)
		}
	}
	names := slices.Sorted(maps.Keys(golden))
	for _, name := range names {
		checkGolden(t, filepath.Join(bodiesOut, name), golden[name])
	}
	if got := goldenFiles(t, bodiesOut); !slices.Equal(got, names) {
		t.Errorf("%s has %q, want %q", bodiesOut, got, names)
	}
}

// limitedBody is Body, with the conversion returned too for the counts of what it read, and checks that it kept to the
// limits.
func limitedBody(t *testing.T, imp *Import, text string) (*bodyConv, *BodyResult) {
	t.Helper()
	return limitedItem(t, imp, bodyItem(text))
}

// limitedItem is limitedBody for the item, such as one that has a link.
func limitedItem(t *testing.T, imp *Import, item *wxr.Item) (*bodyConv, *BodyResult) {
	t.Helper()
	site := bodySite(false)
	c := newBodyConv(imp, BodyOptions{Site: site, Item: item, Target: bodyTarget, Key: bodyKey})
	blocks, more := c.run()
	c.flush(blocks)
	res := &BodyResult{Blocks: blocks, More: more}
	checkBody(t, imp, site, res)
	if n := countBlocks(res.Blocks); n > content.MaxBlocks {
		t.Errorf("the conversion made %d blocks, at most %d are allowed", n, content.MaxBlocks)
	}
	if c.blocks > maxWPBlocks+1 || c.blocks > maxWPBlocks && !c.stopped {
		t.Errorf("the conversion read %d blocks of WordPress (max %d) and stopped = %v", c.blocks, maxWPBlocks, c.stopped)
	}
	if c.htmlBytes > maxHTMLBytes && !c.stopped {
		t.Errorf("the conversion read %d bytes of HTML (max %d) and did not stop", c.htmlBytes, maxHTMLBytes)
	}
	return c, res
}

func stopLine(at string) string {
	return fmt.Sprintf("skipped: the rest of the content is not imported: the conversion reads at most %d blocks and %d bytes of HTML -> %s",
		maxWPBlocks, maxHTMLBytes, at)
}

// patternsExport is the WXR of a site with the synced patterns 1 to n, whose contents are what text makes of their numbers.
func patternsExport(n int, text func(i int) string) *wxr.Export {
	e := &wxr.Export{Channel: wxr.Channel{Link: "https://example.com"}}
	for i := 1; i <= n; i++ {
		e.Items = append(e.Items, wxr.Item{ID: int64(i), Type: "wp_block", Status: "publish", Title: fmt.Sprintf("Pattern %d", i), Content: text(i)})
	}
	return e
}

// TestBodiesLimits converts contents that are too much for the conversion, with the converters of the blocks: it stops or
// does not take the content, says so in the report, and keeps to the limits of the document and of what it reads.
func TestBodiesLimits(t *testing.T) {
	t.Run("the conversion of a test is Body", func(t *testing.T) {
		text := wpPara("a") + moreBlock + wpPara("b") + blockA
		c, res := limitedBody(t, bodyImport(), text)
		again, imp := convertBody(t, bodySite(false), text)
		if bodyDocument(t, res) != bodyDocument(t, again) || c.blocks != 4 {
			t.Errorf("limitedBody gave %s with %d blocks read, Body %s", bodyDocument(t, res), c.blocks, bodyDocument(t, again))
		}
		if a, b := reportText(t, c.imp.Report), reportText(t, imp.Report); a != b {
			t.Errorf("limitedBody gave the report\n%s\nBody\n%s", a, b)
		}
	})

	t.Run("more blocks of WordPress than the limit", func(t *testing.T) {
		imp := bodyImport()
		c, res := limitedBody(t, imp, strings.Repeat(blockA, maxWPBlocks+1))
		sameBlocks(t, res.Blocks, nil)
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf("skipped: the content is not imported: it has more than %d blocks -> /body", maxWPBlocks)))
		if c.blocks != 0 {
			t.Errorf("the conversion converted %d blocks of a content that it does not take", c.blocks)
		}

		// The most is converted, and the document keeps its 10000 blocks.
		imp = bodyImport()
		c, res = limitedBody(t, imp, strings.Repeat(blockA, maxWPBlocks))
		if c.blocks != maxWPBlocks || c.stopped || len(res.Blocks) != content.MaxBlocks {
			t.Errorf("%d blocks gave %d read, stopped = %v, and %d blocks, want all read and %d blocks", maxWPBlocks, c.blocks, c.stopped,
				len(res.Blocks), content.MaxBlocks)
		}
		lines := reportText(t, imp.Report)
		cut := bodyLine(fmt.Sprintf("skipped: the blocks from /body/%d on are not imported: a document has at most %d blocks -> /body",
			content.MaxBlocks, content.MaxBlocks))
		if !strings.Contains(lines, cut+"\n") || strings.Count(lines, "\n") != content.MaxBlocks+1 {
			t.Errorf("the report has %d lines and not %q, want one for each block and that", strings.Count(lines, "\n"), cut)
		}
	})

	t.Run("more paragraphs than a document has blocks", func(t *testing.T) {
		imp := bodyImport()
		c, res := limitedBody(t, imp, strings.Repeat(wpPara("x")+"\n", content.MaxBlocks+1))
		if len(res.Blocks) != content.MaxBlocks || c.blocks != content.MaxBlocks+1 {
			t.Errorf("%d paragraphs gave %d blocks with %d read, want %d and all read", content.MaxBlocks+1, len(res.Blocks), c.blocks,
				content.MaxBlocks)
		}
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf("skipped: the blocks from /body/%d on are not imported: a document has at most %d blocks -> /body",
			content.MaxBlocks, content.MaxBlocks)))
	})

	t.Run("groups in groups", func(t *testing.T) {
		nest := func(n int) string {
			return strings.Repeat(`<!-- wp:group --><div class="wp-block-group">`, n) + wpPara("x") + strings.Repeat(`</div><!-- /wp:group -->`, n)
		}
		depth := func(bs content.Blocks) int {
			n := 0
			for ; len(bs) > 0; bs = bs[len(bs)-1].Children {
				n++
			}
			return n
		}
		// 31 groups hold a paragraph at the 32nd level, the deepest that a document can have.
		imp := bodyImport()
		c, res := limitedBody(t, imp, nest(content.MaxDepth-1))
		if depth(res.Blocks) != content.MaxDepth || c.blocks != content.MaxDepth {
			t.Errorf("31 groups gave %d levels with %d blocks read, want %d and %d", depth(res.Blocks), c.blocks, content.MaxDepth, content.MaxDepth)
		}
		wantLines(t, imp.Report, nil)

		// More groups: the group at the 32nd level is a legacy block of its text, and the recursion ends there.
		for _, n := range []int{content.MaxDepth + 1, 3000} {
			imp = bodyImport()
			c, res = limitedBody(t, imp, nest(n))
			if depth(res.Blocks) != content.MaxDepth {
				t.Errorf("%d groups gave %d levels, want %d", n, depth(res.Blocks), content.MaxDepth)
			}
			if c.blocks > content.MaxDepth+1 {
				t.Errorf("%d groups made the conversion read %d blocks, want at most %d", n, c.blocks, content.MaxDepth+1)
			}
			wantLines(t, imp.Report, bodyLines(legacyLine(deepReason, "/body/0"+strings.Repeat("/content/0", content.MaxDepth-1))))
		}
	})

	t.Run("a paragraph in a hundred thousand elements", func(t *testing.T) {
		// Nothing in the conversion recurses, so the text is read, and a bold inside a bold is one bold.
		const depth = 100000
		text := wpBlock("paragraph", "", "<p>"+strings.Repeat("<b>", depth)+"x"+strings.Repeat("</b>", depth)+"</p>")
		imp := bodyImport()
		c, res := limitedBody(t, imp, text)
		got, err := res.Blocks.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		want := withIDs(jsonList(jsonBlock("paragraph", 0, "", "", `[{"type":"text","text":"x","marks":[{"type":"bold"}]}]`)))
		if string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
		wantLines(t, imp.Report, nil)
		if c.htmlBytes != len("\n<p>")+depth*len("<b>")+len("x")+depth*len("</b>")+len("</p>\n") {
			t.Errorf("the conversion read %d bytes of HTML", c.htmlBytes)
		}
	})

	t.Run("HTML that the parser refuses", func(t *testing.T) {
		// The end tags of p make the parser look through all the open elements again and again.
		markup := "<iframe></iframe>" + strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000)
		text := "<!-- wp:html -->" + markup + "<!-- /wp:html -->"
		imp := bodyImport()
		_, res := limitedBody(t, imp, text)
		const reason = "the HTML of the block is too complex to read"
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], text, reason)})
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
	})

	t.Run("a block with more HTML than one block may have", func(t *testing.T) {
		// The HTML of the block is not read, and does not count among the bytes that the conversion reads: if it did, the
		// last paragraph would be over that limit.
		lowerLimit(t, &maxBlockHTMLBytes, 40)
		lowerLimit(t, &maxHTMLBytes, 80)
		text := strings.Repeat("a", 31)
		fits := wpPara(text) // "\n<p>", the text and "</p>\n"
		over := wpPara(text + "b")
		imp := bodyImport()
		c, res := limitedBody(t, imp, over+fits+fits)
		const reason = "the HTML of the block has more than 40 bytes"
		got, err := res.Blocks.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if want := withIDs(jsonList(jsonLegacy(0, over, reason), jsonPara(1, text), jsonPara(2, text))); string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
		if c.htmlBytes != 80 || c.stopped {
			t.Errorf("the conversion read %d bytes of HTML and stopped = %v, want 80 and false", c.htmlBytes, c.stopped)
		}

		// The HTML of the inner blocks is not that of the block.
		imp = bodyImport()
		_, res = limitedBody(t, imp, wpBlock("group", "", `<div class="wp-block-group">`+fits+`</div>`))
		if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockGroup || countOfType(res.Blocks, content.BlockParagraph) != 1 {
			t.Errorf("a group of a paragraph became %s", bodyDocument(t, res))
		}
		wantLines(t, imp.Report, nil)
	})

	t.Run("an old list of a hundred thousand items", func(t *testing.T) {
		// The items are parts that the conversion makes of the elements of one block. They count among the blocks that it
		// converts, so that it stops, and not after it has made a hundred thousand of them.
		text := wpBlock("list", "", "<ul>"+strings.Repeat("<li>x</li>", 100000)+"</ul>")
		imp := bodyImport()
		c, res := limitedBody(t, imp, text)
		if !c.stopped || c.ids.n > maxWPBlocks {
			t.Errorf("the conversion stopped = %v and made %d blocks with IDs, want true and at most %d", c.stopped, c.ids.n, maxWPBlocks)
		}
		sameBlocks(t, res.Blocks, nil)
		lines := reportText(t, imp.Report)
		for _, want := range []string{
			bodyLine(stopLine("/body")),
			bodyLine("skipped: the blocks from /body/0 on are not imported: a document has at most 10000 blocks -> /body"),
		} {
			if !strings.Contains(lines, want+"\n") {
				t.Errorf("the report has no line %q", want)
			}
		}
	})

	t.Run("a synced pattern of a long list, copied many times", func(t *testing.T) {
		// Each copy converts all the items again, and the items count each time.
		var items strings.Builder
		for range 20000 {
			items.WriteString(wpBlock("list-item", "", "<li>x</li>"))
		}
		pattern := wpBlock("list", "", `<ul class="wp-block-list">`+items.String()+"</ul>")
		e := patternsExport(1, func(int) string { return pattern })
		imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
		c, _ := limitedBody(t, imp, strings.Repeat(`<!-- wp:block {"ref":1} /-->`, 5))
		if !c.stopped || c.ids.n > maxWPBlocks {
			t.Errorf("the conversion stopped = %v and made %d blocks with IDs, want true and at most %d", c.stopped, c.ids.n, maxWPBlocks)
		}
		if lines := reportText(t, imp.Report); !strings.Contains(lines, bodyLine(stopLine("/body"))+"\n") {
			t.Errorf("the report has no line that says the conversion stopped:\n%.2000s", lines)
		}
	})

	t.Run("synced patterns in a chain that is deeper than blocks can be", func(t *testing.T) {
		// Each pattern is a copy of the next: the copies are as deep as the chain, so the chain is cut at the depth that
		// blocks have, and the copies do not take time as long as the chain times the size of its end.
		const length = 40
		e := patternsExport(length, func(i int) string {
			if i == length {
				return wpPara("x")
			}
			return fmt.Sprintf(`<!-- wp:block {"ref":%d} /-->`, i+1)
		})
		imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
		c, res := limitedBody(t, imp, `<!-- wp:block {"ref":1} /-->`)
		const reason = "the synced patterns are nested more than 32 deep"
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], `<!-- wp:block {"ref":33} /-->`, reason)})
		if c.blocks > content.MaxDepth+1 {
			t.Errorf("the conversion converted %d blocks of WordPress, want at most %d", c.blocks, content.MaxDepth+1)
		}
		lines := reportText(t, imp.Report)
		if n := strings.Count(lines, " is copied here; "); n != content.MaxDepth {
			t.Errorf("%d patterns were copied, want %d", n, content.MaxDepth)
		}
		if want := bodyLine(legacyLine(reason, "/body/0")); !strings.Contains(lines, want+"\n") {
			t.Errorf("the report has no line %q", want)
		}
	})

	t.Run("a content over the size limit", func(t *testing.T) {
		imp := bodyImport()
		text := strings.Repeat("x", maxBodyBytes+1)
		c, res := limitedBody(t, imp, text)
		sameBlocks(t, res.Blocks, nil)
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf("skipped: the content is not imported: it is too large: %d bytes (max %d) -> /body",
			len(text), maxBodyBytes)))
		if c.blocks != 0 || c.htmlBytes != 0 {
			t.Errorf("the conversion read %d blocks and %d bytes of a content that it does not take", c.blocks, c.htmlBytes)
		}
	})

	t.Run("a synced pattern that copies the next ten times in a chain", func(t *testing.T) {
		// Pattern 1 holds ten copies of 2, which holds ten copies of 3, and so on to 10, which has a paragraph: 10^9 paragraphs.
		const length = 10
		e := patternsExport(length, func(i int) string {
			if i == length {
				return wpPara("x")
			}
			return strings.Repeat(fmt.Sprintf(`<!-- wp:block {"ref":%d} /-->`, i+1), 10)
		})
		imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
		c, res := limitedBody(t, imp, `<!-- wp:block {"ref":1} /-->`)
		if !c.stopped || c.blocks != maxWPBlocks+1 {
			t.Errorf("the chain made the conversion read %d blocks, stopped = %v, want %d and true", c.blocks, c.stopped, maxWPBlocks+1)
		}
		// It stopped at the limit of the blocks it reads, having made more blocks than a document can have.
		if len(res.Blocks) != content.MaxBlocks {
			t.Errorf("the chain gave %d blocks, want %d", len(res.Blocks), content.MaxBlocks)
		}
		lines := reportText(t, imp.Report)
		for _, want := range []string{
			bodyLine(stopLine("/body")),
			bodyLine(fmt.Sprintf("skipped: the blocks from /body/%d on are not imported: a document has at most %d blocks -> /body",
				content.MaxBlocks, content.MaxBlocks)),
		} {
			if !strings.Contains(lines, want+"\n") {
				t.Errorf("the report has no line %q", want)
			}
		}
	})

	t.Run("a synced pattern that is big, copied many times", func(t *testing.T) {
		// The size of a pattern counts as HTML that the conversion reads each time it is copied; the limit is lowered so that
		// 40 copies of 90 KB and of the paragraph in it are over it.
		lowerLimit(t, &maxHTMLBytes, 1<<20)
		pattern := wpPara(strings.Repeat("あ", 30000))
		e := patternsExport(1, func(int) string { return pattern })
		imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
		c, res := limitedBody(t, imp, strings.Repeat(`<!-- wp:block {"ref":1} /-->`, 40))
		if !c.stopped || c.htmlBytes <= maxHTMLBytes {
			t.Errorf("the conversion read %d bytes of HTML (max %d) and stopped = %v, want it over the limit and stopped", c.htmlBytes,
				maxHTMLBytes, c.stopped)
		}
		// Five copies fit. The paragraph of the sixth goes over the limit, and is a legacy block of its text.
		last := len(res.Blocks) - 1
		if last != 5 || res.Blocks[last].Type != content.BlockLegacy {
			t.Errorf("the conversion gave %d blocks, want five paragraphs and the legacy block of the paragraph that went over the limit",
				len(res.Blocks))
		} else if a, reason := res.Blocks[last].Attrs, fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes); a.HTML != pattern || a.Reason != reason {
			t.Errorf("the last block has the reason %q, want %q and the paragraph of the pattern", a.Reason, reason)
		}
		if lines := reportText(t, imp.Report); !strings.Contains(lines, bodyLine(stopLine("/body"))+"\n") {
			t.Errorf("the report has no line that says the conversion stopped:\n%.2000s", lines)
		}
	})
}

// countOfType returns the number of blocks of the types in bs, at any depth.
func countOfType(bs content.Blocks, types ...content.BlockType) int {
	n := 0
	stack := []content.Blocks{bs}
	for len(stack) > 0 {
		list := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for i := range list {
			if slices.Contains(types, list[i].Type) {
				n++
			}
			stack = append(stack, list[i].Children)
		}
	}
	return n
}

// TestBodiesPartsAreCounted checks that the parts that a converter makes by itself, and not by the flow, such as the items of
// a list and the cells of a table, count among the blocks that the conversion converts, as the blocks of WordPress do. One
// block with many parts, or a pattern that has many of them and is copied many times, cannot make more of them than the
// conversion allows.
func TestBodiesPartsAreCounted(t *testing.T) {
	const limit = 30
	var items, accordionItems strings.Builder
	for range 8 {
		items.WriteString(wpBlock("list-item", "", "<li>x</li>"))
	}
	for range 5 {
		accordionItems.WriteString(wpAccordionItem("Title", wpPara("x")))
	}
	listPattern := wpBlock("list", "", `<ul class="wp-block-list">`+items.String()+"</ul>")
	accordionPattern := wpAccordion("", accordionItems.String())
	paragraphs := func(n int) string { return strings.Repeat("<p>x</p>", n) }
	row := "<tr><td>x</td></tr>"
	tablePattern := wpBlock("table", "", `<figure class="wp-block-table"><table><tbody>`+strings.Repeat(row, 8)+"</tbody></table></figure>")
	for _, tt := range []struct {
		name    string
		text    string
		pattern string // the content of the pattern 1, if the text uses it
		made    func(content.Blocks) int
	}{
		{"the li of an old list", wpBlock("list", "", "<ul>"+strings.Repeat("<li>x</li>", 100)+"</ul>"), "",
			func(bs content.Blocks) int { return countOfType(bs, content.BlockListItem) }},
		{"the p of an old quote", wpBlock("quote", "", `<blockquote class="wp-block-quote">`+paragraphs(100)+"</blockquote>"), "",
			func(bs content.Blocks) int { return countOfType(bs, content.BlockParagraph) }},
		{"the p of a pullquote", wpBlock("pullquote", "", `<figure class="wp-block-pullquote"><blockquote>`+paragraphs(100)+"</blockquote></figure>"), "",
			func(bs content.Blocks) int { return countOfType(bs, content.BlockParagraph) }},
		{"the rows of a table", wpBlock("table", "", `<figure class="wp-block-table"><table><tbody>`+strings.Repeat(row, 100)+"</tbody></table></figure>"), "",
			func(bs content.Blocks) int { return countOfType(bs, content.BlockTableRow, content.BlockTableCell) }},
		{"the cells of a table", wpBlock("table", "", `<figure class="wp-block-table"><table><tbody><tr>`+strings.Repeat("<td>x</td>", 100)+"</tr></tbody></table></figure>"), "",
			func(bs content.Blocks) int { return countOfType(bs, content.BlockTableRow, content.BlockTableCell) }},
		{"the items of a list in a copy of a pattern", strings.Repeat(`<!-- wp:block {"ref":1} /-->`, 6), listPattern,
			func(bs content.Blocks) int { return countOfType(bs, content.BlockListItem) }},
		{"the items of an accordion in a copy of a pattern", strings.Repeat(`<!-- wp:block {"ref":1} /-->`, 4), accordionPattern,
			func(bs content.Blocks) int { return countOfType(bs, content.BlockGroup) }},
		{"the rows and cells of a table in a copy of a pattern", strings.Repeat(`<!-- wp:block {"ref":1} /-->`, 3), tablePattern,
			func(bs content.Blocks) int { return countOfType(bs, content.BlockTableRow, content.BlockTableCell) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lowerLimit(t, &maxWPBlocks, limit)
			imp := bodyImport()
			if tt.pattern != "" {
				e := patternsExport(1, func(int) string { return tt.pattern })
				imp = New("site.xml", e, NewReport(Info{File: "site.xml"}))
			}
			c, res := limitedBody(t, imp, tt.text)
			if !c.stopped {
				t.Errorf("the conversion did not stop; it converted %d blocks", c.blocks)
			}
			if n := tt.made(res.Blocks); n > limit {
				t.Errorf("the conversion made %d of the parts, at most %d are allowed", n, limit)
			}
			if lines := reportText(t, imp.Report); !strings.Contains(lines, bodyLine(stopLine("/body"))+"\n") {
				t.Errorf("the report has no line that says the conversion stopped:\n%.2000s", lines)
			}
		})
	}
}

// TestBodiesStopBeforeTheFirstPart checks that a converter that makes no part because the conversion has stopped makes no
// block: it does not say that the block is empty, and a quote, a details block and an item of an accordion do not keep a
// paragraph with nothing in it. Only the note that the conversion stopped is made.
func TestBodiesStopBeforeTheFirstPart(t *testing.T) {
	list := wpBlock("list", "", `<ul class="wp-block-list">`+wpBlock("list-item", "", "<li>x</li>")+"</ul>")
	table := wpBlock("table", "", `<figure class="wp-block-table"><table><tbody><tr><td>x</td></tr></tbody></table></figure>`)
	quote := wpBlock("quote", "", `<blockquote class="wp-block-quote"><p>x</p></blockquote>`)
	pullquote := wpBlock("pullquote", "", `<figure class="wp-block-pullquote"><blockquote><p>x</p></blockquote></figure>`)
	item := `<li><figure><img src="` + photoURL + `" alt="a" data-id="21"/></figure></li>`
	oneItem := wpBlock("list", "", "<ul><li>x</li></ul>")
	details := wpBlock("details", "", `<details class="wp-block-details"><summary>S</summary>`+wpPara("x")+`</details>`)
	// The paragraph or the copy of the pattern, and then the block, are the first two blocks that are converted, so with the
	// limit 2 the first part of the block is the one over it; the first cell of a table is the second part, after its row. A
	// list of one item before the block makes two more, so that with the limit 3 the first block inside details is the one over
	// it, and with 6 the first one in the panel of an accordion item.
	for _, tt := range []struct {
		name    string
		text    string
		pattern string // the content of the pattern 1, if the text uses it
		limit   int
		left    int // the blocks that are left
	}{
		{"the item of an old list", wpPara("a") + wpBlock("list", "", "<ul><li>x</li></ul>"), "", 2, 1},
		{"the item of a list", wpRef(1), list, 2, 0},
		{"the row of a table", wpPara("a") + table, "", 2, 1},
		{"the cell of a table", wpPara("a") + table, "", 3, 1},
		{"the item of an accordion", wpRef(1), wpAccordion("", wpBlock("accordion-item", "", "<div></div>")), 2, 0},
		{"the paragraph of an old quote", wpPara("a") + quote, "", 2, 1},
		{"the block of a quote", wpRef(1), wpBlock("quote", "", `<blockquote class="wp-block-quote">`+wpPara("x")+`</blockquote>`), 2, 0},
		{"the paragraph of a pullquote", wpPara("a") + pullquote, "", 2, 1},
		{"the button of buttons", wpRef(1), wpButtons("", wpButton("", buttonLink("/a", "A"))), 2, 0},
		{"the column of columns", wpRef(1), wpColumns("", wpColumn("")), 2, 0},
		{"the image of a gallery", wpRef(1), wpBlock("gallery", "", `<figure class="wp-block-gallery has-nested-images">`+
			wpBlock("image", `{"id":21}`, `<figure class="wp-block-image"><img src="`+photoURL+`" alt="a" class="wp-image-21"/></figure>`)+`</figure>`), 2, 0},
		{"the item of an old gallery", wpPara("a") + wpBlock("gallery", "", `<ul class="wp-block-gallery">`+item+"</ul>"), "", 2, 1},
		{"the item of an old gallery that has a caption",
			wpPara("a") + wpBlock("gallery", "", `<figure class="wp-block-gallery"><ul>`+item+`</ul><figcaption>Cap</figcaption></figure>`), "", 2, 1},
		{"the block of a group", wpRef(1), wpGroup("", "div", wpPara("x")), 2, 0},
		{"the block of a cover", wpRef(1), wpCover("", "", wpPara("x")), 2, 0},
		{"the second button of buttons, when the first has no text",
			wpRef(1), wpButtons("", wpButton("", `<a class="wp-block-button__link"></a>`), wpButton("", buttonLink("/a", "A"))), 3, 0},
		{"the block of details", oneItem + details, "", 3, 1},
		{"the block in the panel of an accordion item",
			wpBlock("list", "", "<ul><li>x</li><li>x</li><li>x</li></ul>") + wpAccordion("", wpAccordionItem("T", wpPara("x"))), "", 6, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lowerLimit(t, &maxWPBlocks, tt.limit)
			imp := mediaImport(t)
			lines := bodyLines(stopLine("/body"))
			if tt.pattern != "" {
				e := patternsExport(1, func(int) string { return tt.pattern })
				imp = New("site.xml", e, NewReport(Info{File: "site.xml"}))
				lines = append(lines, bodyLine(`changed: the synced pattern 1 ("Pattern 1") is copied here; whiteroom has no shared blocks yet (stage 2) -> /body`))
			}
			c, res := limitedBody(t, imp, tt.text)
			if !c.stopped || len(res.Blocks) != tt.left {
				t.Errorf("the conversion stopped = %v and gave %d blocks, want true and %d", c.stopped, len(res.Blocks), tt.left)
			}
			wantLines(t, imp.Report, lines)
		})
	}

	// A cover with an image in its background is not empty: it stays, with the image, and without the blocks that were not made.
	t.Run("the block of a cover that keeps its background image", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 2)
		imp := mediaImport(t)
		imp.WXR.Items = append(imp.WXR.Items, patternsExport(1, func(int) string {
			return wpCover(`{"id":21,"url":"`+photoURL+`"}`, `<img class="wp-block-cover__image-background wp-image-21" alt="" src="`+photoURL+`"/>`, wpPara("x"))
		}).Items...)
		c, res := limitedBody(t, imp, wpRef(1))
		if !c.stopped || len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockGroup || len(res.Blocks[0].Children) != 1 ||
			res.Blocks[0].Children[0].Type != content.BlockImage {
			t.Errorf("the conversion stopped = %v and gave %s, want a group of the image", c.stopped, bodyDocument(t, res))
		}
		wantLines(t, imp.Report, bodyLines(
			`changed: the synced pattern 1 ("Pattern 1") is copied here; whiteroom has no shared blocks yet (stage 2) -> /body/0`,
			"changed: the cover is a group here; whiteroom has no cover blocks yet (stage 2) -> /body/0", stopLine("/body/0/content")))
	})
}

// convertText converts text as the content of the post 20 of e, in a new import of e, so that it can be converted again.
// mapped says whether the settings with the names of the theme are used. It checks what holds for every content.
func convertText(t *testing.T, e *wxr.Export, text string, allowHTML, mapped bool) (*BodyResult, *Import) {
	t.Helper()
	imp, err := newBodiesImport(e, NewReport(Info{File: "blocks.xml"}))
	if err != nil {
		t.Fatal(err)
	}
	settings := bodiesSettings(allowHTML)
	site, err := imp.Site(settings, nil)
	if err != nil {
		t.Fatalf("Site = %v", err)
	}
	if !mapped {
		settings = nil
	}
	item := *e.Post(20)
	item.Content = text
	res, err := imp.Body(BodyOptions{Site: site, Settings: settings, Item: &item, Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatalf("Body = %v", err)
	}
	checkBody(t, imp, site, res)
	checkRevision(t, imp, site, &item, res)
	checkTargets(t, imp.Report, []bodiesResult{{&item, bodyTarget, res}})
	stack := []content.Blocks{res.Blocks}
	for len(stack) > 0 {
		list := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for i := range list {
			if id := list[i].ID; id != "" {
				if err := content.CheckBlockID(id); err != nil {
					t.Errorf("the block %d of a list has the ID %q: %v", i, id, err)
				}
			}
			stack = append(stack, list[i].Children)
		}
	}
	for _, entry := range imp.Report.Entries() {
		if strings.HasPrefix(entry.Message, "the content is not imported: it does not pass validation") {
			t.Errorf("the safety net of the guard caught the blocks: %s", entry.Message)
		}
	}
	return res, imp
}

// FuzzBody converts any content: it must not panic, the blocks must pass as the body of a draft revision, and converting
// the same content again in a new import must give the same blocks and the same report.
func FuzzBody(f *testing.F) {
	e, _ := readBodies(f)
	for _, it := range bodyPosts(e) {
		f.Add(it.Content, false, false)
		f.Add(it.Content, true, true)
	}
	for _, s := range []string{
		"", " ", "x", "<p>x</p>", "<!--", "<!-- wp:", "<!-- wp:paragraph", "<!-- wp:paragraph -->", "<!-- wp:paragraph /-->",
		"<!-- /wp:paragraph -->", "<!-- wp:paragraph {", `<!-- wp:paragraph {"a":1} -->`, `<!-- wp:paragraph {"a":} -->`,
		"<!-- wp:a --><!-- wp:b --><p>x</p>", "<!-- wp:a /--><!-- /wp:a --><!-- wp:b /-->",
		`<!-- wp:paragraph {"fontSize":"large","style":{"color":{"text":"#fff"}}} --><p>x</p><!-- /wp:paragraph -->`,
		`<!-- wp:block {"ref":41} /-->`, `<!-- wp:block {"ref":41} --><!-- wp:block {"ref":41} /--><!-- /wp:block -->`,
		`<!-- wp:image {"id":50} --><figure class="wp-block-image"><img src="x" class="wp-image-50"/></figure><!-- /wp:image -->`,
		`<!-- wp:list --><ul><li>a<ul><li>b</li></ul></li></ul><!-- /wp:list -->`,
		`<!-- wp:table --><figure><table><tr><td colspan="2">a</td></tr></table></figure><!-- /wp:table -->`,
		`<!-- wp:columns --><div><!-- wp:columns --><div><!-- wp:column /--></div><!-- /wp:columns --></div><!-- /wp:columns -->`,
		`<p><b><i><a href="/x"><span class="has-inline-color">x</span></a></i></b></p>`, "\x00", "\xff", "<!-- wp:\x00 -->",
		// What the fuzzing of the converters found: characters that Body removes leave nothing in a block.
		"\x11", "\v", "\xc2\x85", `<!-- wp:paragraph --><p>` + "\x11" + `</p><!-- /wp:paragraph -->`,
		`<!-- wp:heading --><h1>` + "\x11" + `</h1><!-- /wp:heading -->`, `<!-- wp:verse --><pre>a&#X0C;b</pre><!-- /wp:verse -->`,
		`<!-- wp:buttons --><div><!-- wp:button --><div><a href="/x">` + "\x11" + `</a></div><!-- /wp:button --></div><!-- /wp:buttons -->`,
	} {
		f.Add(s, true, true)
	}
	f.Fuzz(func(t *testing.T, text string, allowHTML, mapped bool) {
		first, one := convertText(t, e, text, allowHTML, mapped)
		second, other := convertText(t, e, text, allowHTML, mapped)
		if a, b := bodyDocument(t, first), bodyDocument(t, second); a != b {
			t.Errorf("the same content gave the blocks\n%s\nand\n%s", a, b)
		}
		if a, b := reportText(t, one.Report), reportText(t, other.Report); a != b {
			t.Errorf("the same content gave the reports\n%s\nand\n%s", a, b)
		}
	})
}
