package wpimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

var (
	mediaTestID = content.FormatID(content.RefMedia, [16]byte{15: 1})
	mediaTestBy = content.FormatID(content.RefUser, [16]byte{15: 2})
)

// mediaTestAt is the post_date and the post_date_gmt of mediaItem.
const mediaTestAt = "2026-10-05T09:45:00+09:00"

func mediaSite() *content.Site {
	return &content.Site{Languages: []string{"ja"}, DefaultLanguage: "ja", TimeZone: "Asia/Tokyo"}
}

// newMediaConv returns a conversion that writes the originals to a new directory, which it returns too.
func newMediaConv(t *testing.T, opts MediaOptions) (*mediaConv, string) {
	t.Helper()
	o, dir := newTestOriginals(t)
	opts.Files = dir
	if opts.By == "" {
		opts.By = mediaTestBy
	}
	imp := New("wordpress.xml", &wxr.Export{}, NewReport(Info{}))
	return &mediaConv{imp: imp, site: mediaSite(), opts: opts, files: o, zone: mustLocation(t, "Asia/Tokyo")}, dir
}

// mediaItem is an attachment that was published at mediaTestAt.
func mediaItem(attached, fileURL, metadata string) *wxr.Item {
	it := attItem(7, attached, fileURL, metadata)
	it.Date, it.DateGMT = "2026-10-05 09:45:00", "2026-10-05 00:45:00"
	return it
}

func withMeta(it *wxr.Item, key, value string) *wxr.Item {
	it.Meta = append(it.Meta, wxr.Meta{Pos: wxr.Pos{Line: 43, Column: 5}, Key: key, Value: value})
	return it
}

// uploads is a copy of the uploads directory with the files of data, by their paths.
func uploads(files map[string][]byte) fstest.MapFS {
	m := make(fstest.MapFS)
	for name, data := range files {
		m[name] = &fstest.MapFile{Data: data}
	}
	return m
}

// convertItem converts it with the files that attachmentFiles tells, and checks what every conversion must be.
func convertItem(t *testing.T, c *mediaConv, it *wxr.Item) mediaResult {
	t.Helper()
	return convertItemAs(t, c, it, mediaTestID)
}

func convertItemAs(t *testing.T, c *mediaConv, it *wxr.Item, id string) mediaResult {
	t.Helper()
	files, edited, note := c.imp.attachmentFiles(it)
	if note != nil {
		t.Fatalf("attachmentFiles has a note: %v", *note)
	}
	res, err := c.convert(context.Background(), it, id, files, edited)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	checkMediaEntries(t, c, it, id, res)
	return res
}

// checkMediaEntries checks what is the same for every result: where the entries come from and go to, and that nothing
// but the result has changed.
func checkMediaEntries(t *testing.T, c *mediaConv, it *wxr.Item, id string, res mediaResult) {
	t.Helper()
	for _, e := range res.entries {
		if e.Source != ItemSource("wordpress.xml", it) {
			t.Errorf("source of %q = %+v, want the attachment", e.Message, e.Source)
		}
		switch {
		case res.media == nil && e.Target != (Target{}):
			t.Errorf("target of %q = %+v, want none: the attachment is not imported", e.Message, e.Target)
		case res.media != nil && e.Target.File != "media/"+id+".json":
			t.Errorf("target file of %q = %q, want media/%s.json", e.Message, e.Target.File, id)
		}
	}
	if n := len(c.imp.Report.Entries()); n != 0 {
		t.Errorf("the report has %d entries, want none: convert returns them", n)
	}
	if len(c.imp.refs) != 0 {
		t.Errorf("the references are %v, want none", c.imp.refs)
	}
}

func mediaLines(entries []Entry) []string {
	var lines []string
	for _, e := range entries {
		s := string(e.Kind) + ": " + e.Message
		if e.Target != (Target{}) {
			s += " -> " + e.Target.File
			if e.Target.Pointer != "" {
				s += "#" + e.Target.Pointer
			}
		}
		lines = append(lines, s)
	}
	return lines
}

// inMedia is the end of the line of an entry that has the target pointer in the medium.
func inMedia(pointer string) string {
	s := " -> media/" + mediaTestID + ".json"
	if pointer != "" {
		s += "#" + pointer
	}
	return s
}

func wantEntries(t *testing.T, res mediaResult, want ...string) {
	t.Helper()
	if got := mediaLines(res.entries); !slices.Equal(got, want) {
		t.Errorf("entries:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func wantMedia(data []byte, mimeType, fileName string, w, h int) content.Media {
	return content.Media{
		ID:         mediaTestID,
		Hash:       sha256Hash(data),
		FileName:   fileName,
		MIMEType:   mimeType,
		Size:       len(data),
		Width:      w,
		Height:     h,
		UploadedAt: mediaTestAt,
		UploadedBy: mediaTestBy,
	}
}

// checkMedia checks that the result is the medium want, which is valid and which JSON gives back.
func checkMedia(t *testing.T, res mediaResult, want content.Media) {
	t.Helper()
	if res.media == nil {
		t.Fatalf("the attachment is not imported: %q", mediaLines(res.entries))
	}
	if !reflect.DeepEqual(*res.media, want) {
		t.Errorf("medium = %.300v\nwant %.300v", *res.media, want)
	}
	if err := res.media.Validate(mediaSite()); err != nil {
		t.Errorf("Validate: %v", err)
	}
	b, err := res.media.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back content.Media
	if err := back.UnmarshalJSON(b); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if !reflect.DeepEqual(back, *res.media) {
		t.Errorf("the JSON gave back %.300v, want %.300v", back, *res.media)
	}
}

// wantNotImported checks that the attachment is not imported, for why, and that no file is left in dir.
func wantNotImported(t *testing.T, res mediaResult, dir, why string) {
	t.Helper()
	if res.media != nil {
		t.Errorf("medium = %.200v, want none", *res.media)
	}
	wantEntries(t, res, "skipped: the attachment is not imported: "+why)
	wantNames(t, dir)
}

// wantFiles checks that dir has the originals with the data, with that content, and nothing else.
func wantFiles(t *testing.T, dir string, datas ...[]byte) {
	t.Helper()
	var names []string
	for _, data := range datas {
		name := strings.TrimPrefix(sha256Hash(data), "sha256:")
		names = append(names, name)
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(got, data) {
			t.Errorf("the original %s = %d bytes, %v; want the data of %d bytes", name, len(got), err, len(data))
		}
	}
	slices.Sort(names)
	wantNames(t, dir, names...)
}

// convertUpload converts an attachment of the file called name, which the copy of the uploads has with the data, after edit
// has changed the item.
func convertUpload(t *testing.T, name string, data []byte, edit func(*wxr.Item)) (mediaResult, string) {
	t.Helper()
	c, dir := newMediaConv(t, MediaOptions{Uploads: uploads(map[string][]byte{"2026/10/" + name: data})})
	it := mediaItem("2026/10/"+name, uploadsURL+"2026/10/"+name, plainMeta)
	if edit != nil {
		edit(it)
	}
	return convertItem(t, c, it), dir
}

func TestMediaConvertFormats(t *testing.T) {
	for _, tt := range []struct {
		name, file, mimeType string
		data                 []byte
		w, h                 int
	}{
		{"PNG", "a.png", "image/png", testPNG(40, 30, 1), 40, 30},
		{"JPEG", "a.jpg", "image/jpeg", testJPEG(16, 8), 16, 8},
		{"GIF", "a.gif", "image/gif", testGIF(12, 9, 1), 12, 9},
		{"PDF", "a.pdf", "application/pdf", testPDF(), 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, dir := convertUpload(t, tt.file, tt.data, nil)
			checkMedia(t, res, wantMedia(tt.data, tt.mimeType, tt.file, tt.w, tt.h))
			wantEntries(t, res)
			wantFiles(t, dir, tt.data)
		})
	}
}

func TestMediaConvertEverything(t *testing.T) {
	data := testPNG(40, 30, 1)
	res, dir := convertUpload(t, "写真.png", data, func(it *wxr.Item) {
		it.Title = "Photo"
		it.Excerpt = "Taken in Tokyo"
		withMeta(it, "_wp_attachment_image_alt", "オフィスの写真")
	})
	want := wantMedia(data, "image/png", "写真.png", 40, 30)
	want.Alt = content.Localized{"ja": "オフィスの写真"}
	want.Caption = content.Localized{"ja": "Taken in Tokyo"}
	checkMedia(t, res, want)
	wantEntries(t, res)
	wantFiles(t, dir, data)
}

func TestMediaConvertScaledAndRotated(t *testing.T) {
	original, other := testJPEG(32, 16), testJPEG(16, 8)
	for _, suffix := range []string{"scaled", "rotated"} {
		t.Run(suffix, func(t *testing.T) {
			name := "office-" + suffix + ".jpg"
			fsys := &countFS{FS: uploads(map[string][]byte{"2026/10/office.jpg": original, "2026/10/" + name: other})}
			c, dir := newMediaConv(t, MediaOptions{Uploads: fsys})
			res := convertItem(t, c, mediaItem("2026/10/"+name, uploadsURL+"2026/10/"+name, metaWith("office.jpg")))
			checkMedia(t, res, wantMedia(original, "image/jpeg", "office.jpg", 32, 16))
			wantEntries(t, res)
			wantFiles(t, dir, original)
			if fsys.opened != 1 || fsys.closed != 1 {
				t.Errorf("opened %d files and closed %d, want only the original image", fsys.opened, fsys.closed)
			}
		})
	}
}

func TestMediaConvertOriginalImageNotUsed(t *testing.T) {
	scaled := testJPEG(16, 8)
	original := testJPEG(32, 16)
	if len(original) <= len(scaled) {
		t.Fatalf("the test needs an original image that is larger than the scaled one: %d, %d bytes", len(original), len(scaled))
	}
	const review = "review: the original image %q is not imported (%s); the file %q that WordPress made of it is imported"
	for _, tt := range []struct {
		name  string
		file  *fstest.MapFile // office.jpg; nil if the copy does not have it
		limit int64           // the most bytes of a file; 0 for the default
		why   string
	}{
		{"not in the copy", nil, 0, "not in the copy of the uploads"},
		{"not in a format of media", &fstest.MapFile{Data: []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00heicmif1")}, 0,
			"cannot read the file: unsupported file format"},
		{"too many pixels", &fstest.MapFile{Data: pngHeader(20000, 20000)}, 0, "too many pixels: 20000x20000 (max 100000000)"},
		{"too large", &fstest.MapFile{Data: original}, int64(len(scaled)),
			fmt.Sprintf("too large: %d bytes (max %d)", len(original), len(scaled))},
		{"a directory", &fstest.MapFile{Mode: fs.ModeDir}, 0, "not a regular file in the copy of the uploads"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.limit != 0 {
				lowerMediaLimit(t, tt.limit)
			}
			fsys := uploads(map[string][]byte{"2026/10/office-scaled.jpg": scaled})
			if tt.file != nil {
				fsys["2026/10/office.jpg"] = tt.file
			}
			c, dir := newMediaConv(t, MediaOptions{Uploads: fsys})
			res := convertItem(t, c, mediaItem("2026/10/office-scaled.jpg", uploadsURL+"2026/10/office-scaled.jpg", metaWith("office.jpg")))
			checkMedia(t, res, wantMedia(scaled, "image/jpeg", "office-scaled.jpg", 16, 8))
			wantEntries(t, res, fmt.Sprintf(review, "office.jpg", tt.why, "office-scaled.jpg")+inMedia(""))
			wantFiles(t, dir, scaled)
		})
	}
}

func TestMediaConvertEditedImage(t *testing.T) {
	original, edited := testJPEG(32, 16), testJPEG(16, 8)
	const name = "office-e1700000000000.jpg"
	fsys := &countFS{FS: uploads(map[string][]byte{"2026/10/office.jpg": original, "2026/10/" + name: edited})}
	c, dir := newMediaConv(t, MediaOptions{Uploads: fsys})
	res := convertItem(t, c, mediaItem("2026/10/"+name, uploadsURL+"2026/10/"+name, metaWith("office.jpg")))
	checkMedia(t, res, wantMedia(edited, "image/jpeg", name, 16, 8))
	wantEntries(t, res, `review: the image was edited in WordPress: the edited file "`+name+`" is imported, not the original image "office.jpg"`+inMedia(""))
	wantFiles(t, dir, edited)
	if fsys.opened != 1 {
		t.Errorf("opened %d files, want only the edited one", fsys.opened)
	}
}

func TestMediaConvertNotImported(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`)
	unreadable := mediaItem("2026/10/a.png", uploadsURL+"2026/10/a.png", "")
	unreadable.Date, unreadable.DateGMT = "0000-00-00 00:00:00", "0000-00-00 00:00:00"
	for _, tt := range []struct {
		name  string
		it    *wxr.Item
		files map[string][]byte
		why   string
	}{
		{"no file of either kind", mediaItem("2026/10/office-scaled.jpg", uploadsURL+"2026/10/office-scaled.jpg", metaWith("office.jpg")), nil,
			`"office.jpg": not in the copy of the uploads; "office-scaled.jpg": not in the copy of the uploads`},
		{"the original is missing and the other is not in a format of media",
			mediaItem("2026/10/office-scaled.jpg", uploadsURL+"2026/10/office-scaled.jpg", metaWith("office.jpg")),
			map[string][]byte{"2026/10/office-scaled.jpg": svg},
			`"office.jpg": not in the copy of the uploads; "office-scaled.jpg": cannot read the file: unsupported file format`},
		{"SVG", mediaItem("2026/10/a.svg", uploadsURL+"2026/10/a.svg", ""), map[string][]byte{"2026/10/a.svg": svg},
			`"a.svg": cannot read the file: unsupported file format`},
		{"an empty file", mediaItem("2026/10/a.png", uploadsURL+"2026/10/a.png", ""), map[string][]byte{"2026/10/a.png": {}},
			`"a.png": empty file`},
		{"no hint of a file", mediaItem("", "", ""), nil, "the WXR tells no file of it"},
		{"a date that is not one", unreadable, map[string][]byte{"2026/10/a.png": testPNG(4, 4, 0)},
			`its post_date "0000-00-00 00:00:00" cannot be read`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, dir := newMediaConv(t, MediaOptions{Uploads: uploads(tt.files)})
			wantNotImported(t, convertItem(t, c, tt.it), dir, tt.why)
		})
	}
}

func TestMediaConvertFailedValidation(t *testing.T) {
	data := testPNG(4, 4, 0)
	c, dir := newMediaConv(t, MediaOptions{Uploads: uploads(map[string][]byte{"2026/10/a.png": data}), By: "x"})
	res := convertItem(t, c, mediaItem("2026/10/a.png", "", ""))
	if res.media != nil {
		t.Errorf("medium = %v, want none", *res.media)
	}
	lines := mediaLines(res.entries)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], `skipped: the attachment is not imported: "a.png": /uploadedBy: `) {
		t.Errorf("entries = %q, want one that says the validation of /uploadedBy failed", lines)
	}
	wantNames(t, dir)
}

func TestMediaConvertContentOverName(t *testing.T) {
	png, pdf, jpeg := testPNG(8, 6, 0), testPDF(), testJPEG(8, 6)
	for _, tt := range []struct {
		name, file string
		data       []byte
		want       content.Media
	}{
		{"a PNG named .jpg", "a.jpg", png, wantMedia(png, "image/png", "a.jpg", 8, 6)},
		{"a PDF named .jpg", "a.jpg", pdf, wantMedia(pdf, "application/pdf", "a.jpg", 0, 0)},
		{"a JPEG named .pdf", "a.pdf", jpeg, wantMedia(jpeg, "image/jpeg", "a.pdf", 8, 6)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, dir := convertUpload(t, tt.file, tt.data, nil)
			checkMedia(t, res, tt.want)
			wantEntries(t, res)
			wantFiles(t, dir, tt.data)
		})
	}
}

func TestMediaConvertAlt(t *testing.T) {
	data := testPNG(10, 10, 0)
	long := strings.Repeat("a", content.MaxTextBytes+1)
	// U+0344 is two bytes, and NFC writes it as U+0308 U+0301, which are four.
	grows := strings.Repeat(string(rune(0x344)), content.MaxTextBytes/2)
	for _, tt := range []struct {
		name, alt string
		want      content.Localized
		entries   []string
	}{
		{"Japanese", "オフィスの写真", content.Localized{"ja": "オフィスの写真"}, nil},
		{"line breaks and tabs", "line 1\nline 2\tline 3", content.Localized{"ja": "line 1 line 2 line 3"},
			[]string{"changed: line breaks and tabs in the alt are spaces here" + inMedia("/alt")}},
		{"only space", " \n\t ", nil, nil},
		{"control characters", "a\x07b", content.Localized{"ja": "ab"}, nil},
		{"too long", long, nil, []string{
			fmt.Sprintf("skipped: the alt is not imported: it is too long: %d bytes (max %d)", len(long), content.MaxTextBytes) + inMedia("/alt")}},
		{"too long in NFC", grows, nil, []string{
			fmt.Sprintf("skipped: the alt is not imported: it is too long: %d bytes (max %d)", 2*len(grows), content.MaxTextBytes) + inMedia("/alt")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := convertUpload(t, "a.png", data, func(it *wxr.Item) { withMeta(it, "_wp_attachment_image_alt", tt.alt) })
			want := wantMedia(data, "image/png", "a.png", 10, 10)
			want.Alt = tt.want
			checkMedia(t, res, want)
			wantEntries(t, res, tt.entries...)
		})
	}
}

func TestMediaConvertCaption(t *testing.T) {
	data := testPNG(10, 10, 0)
	long := strings.Repeat("a", content.MaxTextBytes+1)
	complex := strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000)
	for _, tt := range []struct {
		name, excerpt string
		want          content.Localized
		entries       []string
	}{
		{"text", "Taken in Tokyo", content.Localized{"ja": "Taken in Tokyo"}, nil},
		{"only space", " \n\t ", nil, nil},
		{"line breaks", "line 1\nline 2", content.Localized{"ja": "line 1\nline 2"}, nil},
		{"paragraphs", "<p>One</p><p>Two<br>three</p>", content.Localized{"ja": "One\n\nTwo\nthree"}, nil},
		{"a character reference", "A &amp; B", content.Localized{"ja": "A & B"}, nil},
		{"a link and an element", `<p>See <a href="https://example.com/x">the <strong>site</strong></a></p>`,
			content.Localized{"ja": "See the site"},
			[]string{`changed: kept only the text of the HTML; lost a link to "https://example.com/x", <strong>` + inMedia("/caption")}},
		{"an image", `<img src="/a.png">`, nil,
			[]string{`changed: kept only the text of the HTML; lost an image "/a.png"` + inMedia("/caption")}},
		{"too complex", complex, nil, []string{"skipped: the caption is not imported: it is too complex to read as HTML" + inMedia("/caption")}},
		{"too long", long, nil, []string{
			fmt.Sprintf("skipped: the caption is not imported: it is too long: %d bytes (max %d)", len(long), content.MaxTextBytes) + inMedia("/caption")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := convertUpload(t, "a.png", data, func(it *wxr.Item) { it.Excerpt = tt.excerpt })
			want := wantMedia(data, "image/png", "a.png", 10, 10)
			want.Caption = tt.want
			checkMedia(t, res, want)
			wantEntries(t, res, tt.entries...)
		})
	}
}

func TestMediaConvertPDFTexts(t *testing.T) {
	data := testPDF()
	res, dir := convertUpload(t, "a.pdf", data, func(it *wxr.Item) {
		it.Excerpt = "A caption"
		it.Content = "A description"
		withMeta(it, "_wp_attachment_image_alt", "An alt")
	})
	checkMedia(t, res, wantMedia(data, "application/pdf", "a.pdf", 0, 0))
	wantEntries(t, res,
		"skipped: the caption is not imported; a PDF has no caption in whiteroom"+inMedia(""),
		"skipped: the description is not imported; whiteroom media have no descriptions"+inMedia(""))
	wantFiles(t, dir, data)
}

func TestMediaConvertDescription(t *testing.T) {
	data := testPNG(10, 10, 0)
	for _, tt := range []struct {
		name, content string
		entries       []string
	}{
		{"a description", "<p>The office</p>", []string{"skipped: the description is not imported; whiteroom media have no descriptions" + inMedia("")}},
		{"only space", " \n\t ", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := convertUpload(t, "a.png", data, func(it *wxr.Item) { it.Content = tt.content })
			checkMedia(t, res, wantMedia(data, "image/png", "a.png", 10, 10))
			wantEntries(t, res, tt.entries...)
		})
	}
}

func TestMediaConvertEntriesInOrder(t *testing.T) {
	data := testPNG(10, 10, 0)
	name := "a" + string(rune(0x202e)) + "b.png"
	res, _ := convertUpload(t, name, data, func(it *wxr.Item) {
		it.Excerpt = `<b>Caption</b>`
		it.Content = "Description"
		withMeta(it, "_wp_attachment_image_alt", "two\nlines")
	})
	want := wantMedia(data, "image/png", "ab.png", 10, 10)
	want.Alt = content.Localized{"ja": "two lines"}
	want.Caption = content.Localized{"ja": "Caption"}
	checkMedia(t, res, want)
	wantEntries(t, res,
		"changed: line breaks and tabs in the alt are spaces here"+inMedia("/alt"),
		"changed: kept only the text of the HTML; lost <b>"+inMedia("/caption"),
		"skipped: the description is not imported; whiteroom media have no descriptions"+inMedia(""),
		fmt.Sprintf("changed: the file name %q is %q here: bidirectional formatting characters are removed", name, "ab.png")+inMedia("/fileName"))
}

func TestMediaConvertFileName(t *testing.T) {
	data := testPNG(10, 10, 0)
	long := strings.Repeat("a", 300) + ".png"
	cut := strings.Repeat("a", 251) + ".png"
	bidi := "photo" + string(rune(0x202e)) + "gpj.png"
	for _, tt := range []struct {
		name, want string
		entries    []string
	}{
		{"写真.png", "写真.png", nil},
		{"e" + string(rune(0x301)) + ".png", string(rune(0xe9)) + ".png", nil},
		{bidi, "photogpj.png", []string{
			fmt.Sprintf("changed: the file name %q is %q here: bidirectional formatting characters are removed", bidi, "photogpj.png") + inMedia("/fileName")}},
		{long, cut, []string{
			fmt.Sprintf("changed: the file name %q is %q here: it is cut to 255 bytes", long, cut) + inMedia("/fileName")}},
	} {
		t.Run(fmt.Sprintf("%.20q", tt.name), func(t *testing.T) {
			res, dir := convertUpload(t, tt.name, data, nil)
			checkMedia(t, res, wantMedia(data, "image/png", tt.want, 10, 10))
			wantEntries(t, res, tt.entries...)
			wantFiles(t, dir, data)
		})
	}
}

func TestMediaConvertCannotPublish(t *testing.T) {
	data := pngHeader(70000, 10)
	res, dir := convertUpload(t, "wide.png", data, nil)
	checkMedia(t, res, wantMedia(data, "image/png", "wide.png", 70000, 10))
	wantEntries(t, res, "review: the build cannot make the published files of the image: too long a side: 70000x10 (max 65535); "+
		"it fails if something published uses the image"+inMedia(""))
	wantFiles(t, dir, data)
}

// newMediaServer serves the files by their paths, and records the paths that it is asked for.
func newMediaServer(t *testing.T, files map[string][]byte) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
}

func TestMediaConvertDownload(t *testing.T) {
	const dirPath = "/wp-content/uploads/2026/10/"
	png, original, scaled := testPNG(24, 12, 3), testJPEG(32, 16), testJPEG(16, 8)
	srv, requested := newMediaServer(t, map[string][]byte{
		dirPath + "a.png":             png,
		dirPath + "office.jpg":        original,
		dirPath + "office-scaled.jpg": scaled,
	})
	newConv := func(t *testing.T, fsys fs.FS) (*mediaConv, string) {
		t.Helper()
		c, dir := newMediaConv(t, MediaOptions{Uploads: fsys, Download: newTestDownloader(t)})
		c.home = mustURL(t, srv.URL)
		return c, dir
	}

	t.Run("a file", func(t *testing.T) {
		before := len(requested())
		c, dir := newConv(t, nil)
		res := convertItem(t, c, mediaItem("2026/10/a.png", srv.URL+dirPath+"a.png", plainMeta))
		checkMedia(t, res, wantMedia(png, "image/png", "a.png", 24, 12))
		wantEntries(t, res)
		wantFiles(t, dir, png)
		if got := requested()[before:]; !slices.Equal(got, []string{dirPath + "a.png"}) {
			t.Errorf("requests = %q", got)
		}
	})
	t.Run("the original image of a scaled one", func(t *testing.T) {
		before := len(requested())
		c, dir := newConv(t, nil)
		res := convertItem(t, c, mediaItem("2026/10/office-scaled.jpg", srv.URL+dirPath+"office-scaled.jpg", metaWith("office.jpg")))
		checkMedia(t, res, wantMedia(original, "image/jpeg", "office.jpg", 32, 16))
		wantEntries(t, res)
		wantFiles(t, dir, original)
		if got := requested()[before:]; !slices.Equal(got, []string{dirPath + "office.jpg"}) {
			t.Errorf("requests = %q, want only the original image", got)
		}
	})
	t.Run("the original image is not found", func(t *testing.T) {
		c, dir := newConv(t, nil)
		res := convertItem(t, c, mediaItem("2026/10/office-scaled.jpg", srv.URL+dirPath+"office-scaled.jpg", metaWith("gone.jpg")))
		checkMedia(t, res, wantMedia(scaled, "image/jpeg", "office-scaled.jpg", 16, 8))
		wantEntries(t, res, fmt.Sprintf(`review: the original image "gone.jpg" is not imported (GET %s: 404 Not Found); `+
			`the file "office-scaled.jpg" that WordPress made of it is imported`, srv.URL+dirPath+"gone.jpg")+inMedia(""))
		wantFiles(t, dir, scaled)
	})
	t.Run("not found", func(t *testing.T) {
		c, dir := newConv(t, nil)
		res := convertItem(t, c, mediaItem("2026/10/missing.png", srv.URL+dirPath+"missing.png", ""))
		wantNotImported(t, res, dir, fmt.Sprintf(`"missing.png": GET %s: 404 Not Found`, srv.URL+dirPath+"missing.png"))
	})
	t.Run("not the old site", func(t *testing.T) {
		before := len(requested())
		c, dir := newConv(t, nil)
		res := convertItem(t, c, mediaItem("2026/10/a.png", "http://other.example/wp-content/uploads/2026/10/a.png", ""))
		wantNotImported(t, res, dir, `"a.png": other.example`+errNotHost)
		if got := requested()[before:]; len(got) != 0 {
			t.Errorf("requests = %q, want none", got)
		}
	})
	t.Run("the copy first", func(t *testing.T) {
		inCopy := testPNG(8, 8, 9)
		c, dir := newConv(t, uploads(map[string][]byte{"2026/10/a.png": inCopy}))
		before := len(requested())
		res := convertItem(t, c, mediaItem("2026/10/a.png", srv.URL+dirPath+"a.png", ""))
		checkMedia(t, res, wantMedia(inCopy, "image/png", "a.png", 8, 8))
		if got := requested()[before:]; len(got) != 0 {
			t.Errorf("requests = %q, want none for a file that the copy has", got)
		}
		// Another file is not in the copy, so it is downloaded.
		it := mediaItem("2026/10/office.jpg", srv.URL+dirPath+"office.jpg", "")
		it.ID = 8
		res = convertItemAs(t, c, it, content.FormatID(content.RefMedia, [16]byte{15: 8}))
		if res.media == nil || res.media.Hash != sha256Hash(original) {
			t.Errorf("medium = %v, want the downloaded file", res.media)
		}
		if got := requested()[before:]; !slices.Equal(got, []string{dirPath + "office.jpg"}) {
			t.Errorf("requests = %q, want only the file that the copy does not have", got)
		}
		wantFiles(t, dir, inCopy, original)
	})
}

func TestMediaConvertNoPlaceToRead(t *testing.T) {
	data := testPNG(8, 8, 0)
	copyOnly := MediaOptions{Uploads: uploads(map[string][]byte{"2026/10/a.png": data})}
	for _, tt := range []struct {
		name     string
		download bool
		copy     bool
		f        mediaFile
		why      string
	}{
		{"a copy and no path", false, true, mediaFile{name: "a.png", url: mustURL(t, uploadsURL+"2026/10/a.png")},
			"the WXR does not tell where it is in the uploads directory"},
		{"a download and no address", true, false, mediaFile{name: "a.png", rel: "2026/10/a.png"},
			"the WXR gives no address to download it from"},
		{"a copy without the file and no address", true, true, mediaFile{name: "b.png", rel: "2026/10/b.png"},
			"not in the copy of the uploads; the WXR gives no address to download it from"},
		{"neither a path nor an address", true, true, mediaFile{name: "a.png"},
			"the WXR does not tell where it is in the uploads directory; the WXR gives no address to download it from"},
		{"nowhere to read from", false, false, mediaFile{name: "a.png", rel: "2026/10/a.png"},
			"there is neither a copy of the uploads nor a download to read it from"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := MediaOptions{}
			if tt.copy {
				opts = copyOnly
			}
			if tt.download {
				opts.Download = newTestDownloader(t)
			}
			c, dir := newMediaConv(t, opts)
			it := mediaItem("", "", "")
			res, err := c.convert(context.Background(), it, mediaTestID, []mediaFile{tt.f}, "")
			if err != nil {
				t.Fatal(err)
			}
			checkMediaEntries(t, c, it, mediaTestID, res)
			wantNotImported(t, res, dir, fmt.Sprintf("%q: %s", tt.f.name, tt.why))
		})
	}
}

func TestMediaConvertCanceled(t *testing.T) {
	data := testPNG(8, 8, 0)
	srv, requested := newMediaServer(t, map[string][]byte{"/a.png": data})
	for _, tt := range []struct {
		name string
		opts MediaOptions
	}{
		{"a copy", MediaOptions{Uploads: uploads(map[string][]byte{"2026/10/a.png": data})}},
		{"a copy without the file", MediaOptions{Uploads: uploads(nil)}},
		{"a download", MediaOptions{Download: newTestDownloader(t)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, dir := newMediaConv(t, tt.opts)
			c.home = mustURL(t, srv.URL)
			it := mediaItem("2026/10/a.png", srv.URL+"/a.png", "")
			files, edited, _ := c.imp.attachmentFiles(it)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			res, err := c.convert(ctx, it, mediaTestID, files, edited)
			if !errors.Is(err, context.Canceled) || res.media != nil || len(res.entries) != 0 {
				t.Errorf("convert = %v, %q; want the error of the context and nothing else", err, mediaLines(res.entries))
			}
			wantNames(t, dir)
		})
	}
	if got := requested(); len(got) != 0 {
		t.Errorf("requests = %q, want none", got)
	}
}

// failRead is a file that cannot be read.
type failRead struct{ fs.File }

func (failRead) Read([]byte) (int, error) { return 0, syscall.EIO }

type failReadFS struct{ fs.FS }

func (f failReadFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	return failRead{file}, err
}

func TestMediaConvertStops(t *testing.T) {
	data := testPNG(8, 8, 0)
	for _, tt := range []struct {
		name string
		fsys fs.FS
		prep func(dir string) error
		want string
	}{
		{"the directory is gone", uploads(map[string][]byte{"2026/10/a.png": data}), os.RemoveAll, "saving an original in "},
		{"the copy cannot be opened", errorFS{syscall.EIO}, nil, "reading the copy of the uploads: open 2026/10/a.png: input/output error"},
		{"the copy cannot be read", failReadFS{uploads(map[string][]byte{"2026/10/a.png": data})}, nil,
			"reading the copy of the uploads: input/output error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, dir := newMediaConv(t, MediaOptions{Uploads: tt.fsys})
			if tt.prep != nil {
				if err := tt.prep(dir); err != nil {
					t.Fatal(err)
				}
			}
			it := mediaItem("2026/10/a.png", "", "")
			files, edited, _ := c.imp.attachmentFiles(it)
			res, err := c.convert(context.Background(), it, mediaTestID, files, edited)
			wantStopping(t, err)
			if !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to start with %q", err, tt.want)
			}
			if res.media != nil || len(res.entries) != 0 {
				t.Errorf("result = %v, %q; want nothing", res.media, mediaLines(res.entries))
			}
			if tt.prep == nil {
				wantNames(t, dir)
			}
		})
	}
}

func TestMediaConvertConcurrent(t *testing.T) {
	const n = 16
	files := make(map[string][]byte)
	datas := make([][]byte, n)
	for i := range n {
		datas[i] = testPNG(8+i, 8, byte(i))
		files[fmt.Sprintf("2026/10/%d.png", i)] = datas[i]
	}
	c, dir := newMediaConv(t, MediaOptions{Uploads: uploads(files)})
	results := make([]mediaResult, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			it := mediaItem(fmt.Sprintf("2026/10/%d.png", i), "", "")
			it.ID = int64(100 + i)
			planned, _, _ := c.imp.attachmentFiles(it)
			res, err := c.convert(context.Background(), it, content.FormatID(content.RefMedia, [16]byte{15: byte(i + 1)}), planned, "")
			if err != nil {
				t.Errorf("convert %d: %v", i, err)
			}
			results[i] = res
		}()
	}
	wg.Wait()
	for i, res := range results {
		if res.media == nil || res.media.Hash != sha256Hash(datas[i]) || res.media.Width != 8+i || len(res.entries) != 0 {
			t.Errorf("result %d = %v, %q", i, res.media, mediaLines(res.entries))
		}
	}
	wantFiles(t, dir, datas...)
	if n := len(c.imp.Report.Entries()); n != 0 {
		t.Errorf("the report has %d entries, want none", n)
	}
}

func TestMediaConvertSameFileTwice(t *testing.T) {
	data := testPNG(8, 8, 0)
	c, dir := newMediaConv(t, MediaOptions{Uploads: uploads(map[string][]byte{"2026/10/a.png": data, "2026/10/b.png": data})})
	one := convertItem(t, c, mediaItem("2026/10/a.png", "", ""))
	two := convertItemAs(t, c, mediaItem("2026/10/b.png", "", ""), content.FormatID(content.RefMedia, [16]byte{15: 2}))
	if one.media == nil || two.media == nil || one.media.Hash != two.media.Hash || one.media.FileName == two.media.FileName {
		t.Errorf("media = %v, %v; want the same file under two names", one.media, two.media)
	}
	wantFiles(t, dir, data)
}

func TestMediaConvertReadsNothingElse(t *testing.T) {
	// Only the files of the attachment are read: not the sizes that the metadata lists.
	data := testJPEG(32, 16)
	meta := phpArray(phpString("width"), phpInt(32), phpString("height"), phpInt(16), phpString("file"), phpString("2026/10/a.jpg"),
		phpString("sizes"), phpArray(phpString("thumbnail"), phpArray(phpString("file"), phpString("a-150x150.jpg"))))
	fsys := &countFS{FS: uploads(map[string][]byte{"2026/10/a.jpg": data, "2026/10/a-150x150.jpg": testJPEG(8, 4)})}
	c, dir := newMediaConv(t, MediaOptions{Uploads: fsys})
	res := convertItem(t, c, mediaItem("2026/10/a.jpg", "", meta))
	checkMedia(t, res, wantMedia(data, "image/jpeg", "a.jpg", 32, 16))
	if fsys.opened != 1 {
		t.Errorf("opened %d files, want 1", fsys.opened)
	}
	wantFiles(t, dir, data)
}
