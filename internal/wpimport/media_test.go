package wpimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
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

// TestMediaConvertDownloadFallsBack checks that an original image that the download cannot read, after all the attempts or
// not, leaves the file that WordPress made of it to be tried.
func TestMediaConvertDownloadFallsBack(t *testing.T) {
	const dirPath = "/wp-content/uploads/2026/10/"
	scaled := testJPEG(16, 8)
	for _, tt := range []struct {
		name     string
		original http.HandlerFunc // for office.jpg
		timeout  time.Duration    // of an attempt; 0 for the default
		attempts int              // 0 for the default
		reason   string           // after the address that is asked for, which the entry starts with
		want     int              // requests for the original image
	}{
		{"an error of the server in every attempt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
			0, 0, "503 Service Unavailable (after 3 attempts)", 3},
		{"no answer in time", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() },
			40 * time.Millisecond, 2, "timed out after 40ms (after 2 attempts)", 2},
		{"a redirect to another host", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://other.example/office.jpg", http.StatusFound)
		}, 0, 0, "redirected to http://other.example/office.jpg, which is not on the old site or an allowed host", 1},
		{"a single attempt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) },
			0, 1, "502 Bad Gateway (after 1 attempt)", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.URL.Path == dirPath+"office.jpg" {
					tt.original(w, r)
					return
				}
				_, _ = w.Write(scaled)
			}))
			t.Cleanup(srv.Close)
			d := newTestDownloader(t)
			d.Timeout, d.Attempts = tt.timeout, tt.attempts
			c, dir := newMediaConv(t, MediaOptions{Download: d})
			c.home = mustURL(t, srv.URL)
			res := convertItem(t, c, mediaItem("2026/10/office-scaled.jpg", srv.URL+dirPath+"office-scaled.jpg", metaWith("office.jpg")))
			checkMedia(t, res, wantMedia(scaled, "image/jpeg", "office-scaled.jpg", 16, 8))
			wantEntries(t, res, fmt.Sprintf(`review: the original image "office.jpg" is not imported (GET %s: %s); `+
				`the file "office-scaled.jpg" that WordPress made of it is imported`, srv.URL+dirPath+"office.jpg", tt.reason)+inMedia(""))
			wantFiles(t, dir, scaled)
			mu.Lock()
			defer mu.Unlock()
			want := slices.Repeat([]string{dirPath + "office.jpg"}, tt.want)
			want = append(want, dirPath+"office-scaled.jpg")
			if !slices.Equal(paths, want) {
				t.Errorf("requests = %q, want %q", paths, want)
			}
		})
	}
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

// uploadItem is an attachment with the ID id and the title, whose file is called name in the uploads.
func uploadItem(id int64, name, title string) *wxr.Item {
	it := mediaItem("2026/10/"+name, uploadsURL+"2026/10/"+name, "")
	it.ID, it.Title = id, title
	return it
}

func uploadExport(link string, items ...*wxr.Item) *wxr.Export {
	e := &wxr.Export{Channel: wxr.Channel{Link: link}}
	for _, it := range items {
		e.Items = append(e.Items, *it)
	}
	return e
}

// newPlannedImport is the import of e that has made its plan for the media, as the caller of Media does.
func newPlannedImport(t *testing.T, e *wxr.Export) *Import {
	t.Helper()
	imp := New("wordpress.xml", e, NewReport(Info{}))
	if err := imp.PlanMedia(); err != nil {
		t.Fatal(err)
	}
	return imp
}

// newSampleMediaImport is the import of testdata/media.xml, with its plan if plan is true.
func newSampleMediaImport(t *testing.T, plan bool) *Import {
	t.Helper()
	e, rep := readString(t, "media.xml", string(readTestdata(t, "media.xml")))
	if n := len(rep.Entries()); n != 0 {
		t.Fatalf("reading media.xml gives %d entries, want none", n)
	}
	imp := New("media.xml", e, rep)
	if plan {
		if err := imp.PlanMedia(); err != nil {
			t.Fatal(err)
		}
	}
	return imp
}

func filesDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "files")
}

// mediaState tells what the table and the references have of each attachment.
func mediaState(imp *Import) string {
	var b strings.Builder
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		kind, id, ok := imp.IDs.Lookup(PostObject(it))
		ref, refOK := imp.Ref(content.RefMedia, id)
		fmt.Fprintf(&b, "%d: %q %q %v %+v %v\n", it.ID, kind, id, ok, ref, refOK)
	}
	fmt.Fprintf(&b, "%d references\n", len(imp.refs))
	return b.String()
}

func mediaJSON(t *testing.T, docs []content.Media) string {
	t.Helper()
	var b strings.Builder
	for _, m := range docs {
		data, err := m.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

// entryLines writes the entries as lines that do not depend on the place in the WXR.
func entryLines(entries []Entry) []string {
	var lines []string
	for _, e := range entries {
		s := string(e.Kind) + " " + e.Source.Record
		if e.Source.ID != 0 {
			s += " " + strconv.FormatInt(e.Source.ID, 10)
		}
		if e.Source.Meta != "" {
			s += " " + e.Source.Meta
		}
		s += ": " + e.Message
		if e.Target.File != "" {
			s += " -> " + e.Target.File + "#" + e.Target.Pointer
		}
		lines = append(lines, s)
	}
	return lines
}

// runMedia calls Media with the site of mediaSite, and with the default By if opts has none.
func runMedia(t *testing.T, imp *Import, opts MediaOptions) []content.Media {
	t.Helper()
	if opts.By == "" {
		opts.By = mediaTestBy
	}
	docs, err := imp.Media(context.Background(), mediaSite(), opts)
	if err != nil {
		t.Fatalf("Media: %v", err)
	}
	return docs
}

// mediaSampleCopy is a copy of the uploads for testdata/media.xml. The files that the sample names and that are not here
// are the original image beach.heic, and missing.png.
func mediaSampleCopy() map[string][]byte {
	logo := testPNG(60, 20, 1)
	return map[string][]byte{
		"2026/10/office.jpg":                     testJPEG(32, 16),
		"2026/10/office-scaled.jpg":              testJPEG(16, 8),
		"2026/10/store.jpg":                      testJPEG(8, 12),
		"2026/10/store-rotated.jpg":              testJPEG(12, 8),
		"2026/10/logo.png":                       logo,
		"2026/10/logo-copy.png":                  logo,
		"2026/10/anim.gif":                       testGIF(12, 9, 2),
		"2026/10/guide.pdf":                      testPDF(),
		"2026/10/team.jpg":                       testJPEG(40, 20),
		"2026/10/team-scaled-e1760000000123.jpg": testJPEG(20, 10),
		"2026/10/beach-scaled.jpg":               testJPEG(10, 5),
		"2026/10/diagram.svg":                    []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`),
		"2026/10/old.png":                        testPNG(8, 8, 5),
		"2026/10/写真.png":                         testPNG(10, 20, 7),
	}
}

func TestMediaSample(t *testing.T) {
	imp := newSampleMediaImport(t, true)
	ids := make(map[int64]string)
	for i := range imp.WXR.Items {
		if _, id, ok := imp.IDs.Lookup(PostObject(&imp.WXR.Items[i])); ok {
			ids[imp.WXR.Items[i].ID] = id
		}
	}
	if len(ids) != 11 {
		t.Fatalf("the plan has %d media, want the 11 that are not in the trash", len(ids))
	}
	fsys := mediaSampleCopy()
	dir := filesDir(t)
	docs := runMedia(t, imp, MediaOptions{Files: dir, Uploads: uploads(fsys), Workers: 3})

	type want struct {
		post         int64
		file         string // in the uploads, and the fileName
		mimeType     string
		w, h         int
		alt, caption string
		time         string
	}
	wants := []want{
		{201, "office.jpg", "image/jpeg", 32, 16, "オフィスの外観", "本社の外観です。", "09:00"},
		{202, "store.jpg", "image/jpeg", 8, 12, "", "", "09:10"},
		{203, "logo.png", "image/png", 60, 20, "会社のロゴ", "", "09:20"},
		{204, "anim.gif", "image/gif", 12, 9, "", "", "09:30"},
		{205, "guide.pdf", "application/pdf", 0, 0, "", "", "09:40"},
		{206, "logo-copy.png", "image/png", 60, 20, "", "", "09:50"},
		{207, "team-scaled-e1760000000123.jpg", "image/jpeg", 20, 10, "チームの 集合写真", "", "10:00"},
		{208, "beach-scaled.jpg", "image/jpeg", 10, 5, "", "", "10:10"},
		{212, "写真.png", "image/png", 10, 20, "", "", "10:50"},
	}
	var wantDocs []content.Media
	var datas [][]byte
	for _, w := range wants {
		data := fsys["2026/10/"+w.file]
		m := content.Media{
			ID: ids[w.post], Hash: sha256Hash(data), FileName: w.file, MIMEType: w.mimeType, Size: len(data), Width: w.w, Height: w.h,
			UploadedAt: "2026-10-01T" + w.time + ":00+09:00", UploadedBy: mediaTestBy,
		}
		if w.alt != "" {
			m.Alt = content.Localized{"ja": w.alt}
		}
		if w.caption != "" {
			m.Caption = content.Localized{"ja": w.caption}
		}
		wantDocs = append(wantDocs, m)
		if !slices.ContainsFunc(datas, func(d []byte) bool { return bytes.Equal(d, data) }) {
			datas = append(datas, data)
		}
	}
	slices.SortFunc(wantDocs, func(a, b content.Media) int { return strings.Compare(a.ID, b.ID) })

	if len(docs) != len(wantDocs) {
		t.Fatalf("Media gives %d documents, want %d", len(docs), len(wantDocs))
	}
	site := mediaSite()
	for i, m := range docs {
		if !reflect.DeepEqual(m, wantDocs[i]) {
			t.Errorf("document %d = %.300v\nwant %.300v", i, m, wantDocs[i])
		}
		if err := m.Validate(site); err != nil {
			t.Errorf("document %d: Validate: %v", i, err)
		}
		data, err := m.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var back content.Media
		if err := back.UnmarshalJSON(data); err != nil || !reflect.DeepEqual(back, m) {
			t.Errorf("document %d: the JSON gives %.300v, %v", i, back, err)
		}
	}

	// The originals are where the site directory has them, with the contents of the files that were imported.
	wantFiles(t, dir, datas...)
	sd, err := sitedir.Open(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sd.Close() }()
	for _, m := range docs {
		f, err := sd.OpenMedia(m.Hash)
		if err != nil {
			t.Errorf("OpenMedia(%s): %v", m.Hash, err)
			continue
		}
		got, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || sha256Hash(got) != m.Hash || len(got) != m.Size {
			t.Errorf("the original of %s is %d bytes, %v", m.ID, len(got), err)
		}
	}

	byPost := func(post int64) content.Media {
		i := slices.IndexFunc(docs, func(m content.Media) bool { return m.ID == ids[post] })
		if i < 0 {
			t.Fatalf("no document for the post %d", post)
		}
		return docs[i]
	}
	if a, b := byPost(203), byPost(206); a.ID == b.ID || a.Hash != b.Hash {
		t.Errorf("the same file twice gives %s %s and %s %s, want two media of one file", a.ID, a.Hash, b.ID, b.Hash)
	}
	if got, want := byPost(201).Hash, sha256Hash(fsys["2026/10/office.jpg"]); got != want {
		t.Errorf("the hash of the scaled image = %s, want that of its original image %s", got, want)
	}

	for _, w := range wants {
		kind, id, ok := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: w.post})
		ref, refOK := imp.Ref(content.RefMedia, ids[w.post])
		wantKind := content.MediaImage
		if w.mimeType == "application/pdf" {
			wantKind = content.MediaPDF
		}
		if !ok || kind != content.RefMedia || id != ids[w.post] || !refOK || ref != (content.Ref{Type: wantKind}) {
			t.Errorf("post %d: Lookup = %q %q %v, Ref = %+v %v; want the medium %s of the kind %s", w.post, kind, id, ok, ref, refOK, ids[w.post], wantKind)
		}
	}
	for _, post := range []int64{209, 210, 211} {
		if _, _, ok := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: post}); ok {
			t.Errorf("post %d is in the table, want it taken out", post)
		}
		if id, planned := ids[post]; planned {
			if _, ok := imp.Ref(content.RefMedia, id); ok {
				t.Errorf("post %d has a reference, want it dropped", post)
			}
		}
	}

	replace := func(s string) string {
		for post, id := range ids {
			s = strings.ReplaceAll(s, fmt.Sprintf("{%d}", post), id)
		}
		return s
	}
	wantLines := []string{
		`skipped channel: the titles of 8 attachments are not imported; whiteroom media have no titles`,
		replace(`skipped item 201: the description is not imported; whiteroom media have no descriptions -> media/{201}.json#`),
		replace(`changed item 201: kept only the text of the HTML; lost <em> -> media/{201}.json#/caption`),
		replace(`skipped item 205: the caption is not imported; a PDF has no caption in whiteroom -> media/{205}.json#`),
		replace(`changed item 207: line breaks and tabs in the alt are spaces here -> media/{207}.json#/alt`),
		replace(`review item 207: the image was edited in WordPress: the edited file "team-scaled-e1760000000123.jpg" is imported, not the original image "team.jpg" -> media/{207}.json#`),
		replace(`review item 208: the original image "beach.heic" is not imported (not in the copy of the uploads); the file "beach-scaled.jpg" that WordPress made of it is imported -> media/{208}.json#`),
		`skipped item 209: the attachment is not imported: "missing.png": not in the copy of the uploads`,
		`skipped item 210: the attachment is not imported: "diagram.svg": cannot read the file: unsupported file format`,
		`skipped item 211: the attachment is not imported: it is in the trash`,
	}
	if got := entryLines(imp.Report.Entries()); !slices.Equal(got, wantLines) {
		t.Errorf("report:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(wantLines, "\n  "))
	}
}

// mediaOutcome is what an import of the media leaves: the documents, the report, the table and the references, and the
// files of the originals.
type mediaOutcome struct {
	docs, report, state string
	files               []string
}

func importOutcome(t *testing.T, imp *Import, copyOf map[string][]byte, workers int) mediaOutcome {
	t.Helper()
	dir := filesDir(t)
	docs := runMedia(t, imp, MediaOptions{Files: dir, Uploads: uploads(copyOf), Workers: workers})
	return mediaOutcome{mediaJSON(t, docs), reportText(t, imp.Report), mediaState(imp), dirNames(t, dir)}
}

func wantSameOutcome(t *testing.T, what string, got, want mediaOutcome) {
	t.Helper()
	switch {
	case got.docs != want.docs:
		t.Errorf("%s: the documents differ:\n%s\nwant:\n%s", what, got.docs, want.docs)
	case got.report != want.report:
		t.Errorf("%s: the report differs:\n%s\nwant:\n%s", what, got.report, want.report)
	case got.state != want.state:
		t.Errorf("%s: the table and the references differ:\n%s\nwant:\n%s", what, got.state, want.state)
	case !slices.Equal(got.files, want.files):
		t.Errorf("%s: the originals are %q, want %q", what, got.files, want.files)
	}
}

// mediaMix is a WXR of 60 attachments whose files are, in turn: a PNG of its own, a JPEG of its own, none, an SVG, a PDF
// that all the PDFs have, and a PNG that every sixth has. The attachment 1007 is in the trash and 1013 is an automatic
// draft.
func mediaMix() (*wxr.Export, map[string][]byte) {
	files := make(map[string][]byte)
	shared := testPNG(9, 9, 1)
	var items []*wxr.Item
	for i := range 60 {
		name := fmt.Sprintf("a%d", i)
		switch i % 6 {
		case 0:
			name += ".png"
			files["2026/10/"+name] = testPNG(4+i, 4, byte(i))
		case 1:
			name += ".jpg"
			files["2026/10/"+name] = testJPEG(8+i, 8)
		case 2:
			name += ".png"
		case 3:
			name += ".svg"
			files["2026/10/"+name] = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`)
		case 4:
			name += ".pdf"
			files["2026/10/"+name] = testPDF()
		case 5:
			name += ".png"
			files["2026/10/"+name] = shared
		}
		it := uploadItem(int64(1000+i), name, fmt.Sprintf("Title %d", i))
		switch i {
		case 7:
			it.Status = "trash"
		case 13:
			it.Status = "auto-draft"
		}
		items = append(items, it)
	}
	return uploadExport("https://example.com", items...), files
}

func TestMediaWorkersDoNotChangeTheResult(t *testing.T) {
	run := func(workers int) mediaOutcome {
		e, files := mediaMix()
		return importOutcome(t, newPlannedImport(t, e), files, workers)
	}
	want := run(1)
	// 40 attachments have a file of a kind that is imported, and 2 of those are not imported as they are in the trash or a
	// draft. The originals are 10 PNGs and 8 JPEGs of their own, and the PDF and the PNG that are shared.
	if n := strings.Count(want.docs, "\n"); n != 38 || len(want.files) != 20 {
		t.Fatalf("the import gives %d documents and %d originals, want 38 and 20", n, len(want.files))
	}
	for _, workers := range []int{0, 2, 3, 8, 100} {
		wantSameOutcome(t, fmt.Sprintf("%d workers", workers), run(workers), want)
	}

	sample := func(workers int) mediaOutcome {
		return importOutcome(t, newSampleMediaImport(t, true), mediaSampleCopy(), workers)
	}
	wantSameOutcome(t, "the sample with 8 workers", sample(8), sample(1))
}

func TestMediaWithoutPlan(t *testing.T) {
	planned := importOutcome(t, newSampleMediaImport(t, true), mediaSampleCopy(), 2)
	unplanned := importOutcome(t, newSampleMediaImport(t, false), mediaSampleCopy(), 2)
	wantSameOutcome(t, "no plan", unplanned, planned)
}

func TestMediaKindFromContents(t *testing.T) {
	imp := newPlannedImport(t, uploadExport("https://example.com", uploadItem(1, "a.jpg", ""), uploadItem(2, "b.pdf", ""), uploadItem(3, "c.png", "")))
	kinds := func() string {
		var got []string
		for post := int64(1); post <= 3; post++ {
			_, id, _ := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: post})
			ref, ok := imp.Ref(content.RefMedia, id)
			got = append(got, fmt.Sprintf("%v/%s", ok, ref.Type))
		}
		return strings.Join(got, " ")
	}
	if got, want := kinds(), "true/image true/pdf true/image"; got != want {
		t.Fatalf("after the plan: %s, want %s, which the names of the files tell", got, want)
	}
	runMedia(t, imp, MediaOptions{Files: filesDir(t), Uploads: uploads(map[string][]byte{
		"2026/10/a.jpg": testPDF(), "2026/10/b.pdf": testJPEG(8, 8), "2026/10/c.png": testPNG(8, 8, 0),
	})})
	if got, want := kinds(), "true/pdf true/image true/image"; got != want {
		t.Errorf("after Media: %s, want %s, which the contents of the files tell", got, want)
	}
}

func TestMediaLogoAndIcon(t *testing.T) {
	const (
		notImportedLogo = "skipped item 2: the logo, attachment 2, is not imported -> site/settings.json#/logo"
		notImportedIcon = "skipped item 2: the icon, attachment 2, is not imported -> site/settings.json#/icon"
		pdfLogo         = "skipped item 3: the logo, attachment 3, is not an image -> site/settings.json#/logo"
		pdfIcon         = "skipped item 3: the icon, attachment 3, is not an image -> site/settings.json#/icon"
	)
	for _, tt := range []struct {
		name        string
		logo, icon  string // ok (image), gone (not imported), pdf, other (not of the import) or none
		wantLogo    string
		wantIcon    string
		wantEntries []string
	}{
		{"images", "ok", "ok", "ok", "ok", nil},
		{"the logo is not imported", "gone", "ok", "", "ok", []string{notImportedLogo}},
		{"the icon is not imported", "ok", "gone", "ok", "", []string{notImportedIcon}},
		{"the icon is a PDF", "ok", "pdf", "ok", "", []string{pdfIcon}},
		{"the logo is a PDF", "pdf", "ok", "", "ok", []string{pdfLogo}},
		{"both are one attachment that is not imported", "gone", "gone", "", "", []string{notImportedIcon, notImportedLogo}},
		{"a PDF and one that is not imported", "pdf", "gone", "", "", []string{notImportedIcon, pdfLogo}},
		{"media that the import has not made", "other", "other", "other", "other", nil},
		{"none", "none", "none", "none", "none", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := newPlannedImport(t, uploadExport("https://example.com", uploadItem(1, "ok.png", ""), uploadItem(2, "gone.png", ""), uploadItem(3, "doc.pdf", "")))
			other := content.FormatID(content.RefMedia, [16]byte{15: 99})
			id := func(key string) string {
				switch key {
				case "ok", "gone", "pdf":
					post := map[string]int64{"ok": 1, "gone": 2, "pdf": 3}[key]
					_, id, _ := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: post})
					return id
				case "other":
					return other
				}
				return ""
			}
			site := mediaSite()
			site.Logo, site.Icon = id(tt.logo), id(tt.icon)
			wantLogo, wantIcon := id(tt.wantLogo), id(tt.wantIcon)
			files := uploads(map[string][]byte{"2026/10/ok.png": testPNG(8, 8, 0), "2026/10/doc.pdf": testPDF()})
			docs, err := imp.Media(context.Background(), site, MediaOptions{Files: filesDir(t), Uploads: files, By: mediaTestBy})
			if err != nil {
				t.Fatal(err)
			}
			if len(docs) != 2 {
				t.Errorf("Media gives %d documents, want the image and the PDF", len(docs))
			}
			if site.Logo != wantLogo || site.Icon != wantIcon {
				t.Errorf("logo = %q, icon = %q; want %q and %q", site.Logo, site.Icon, wantLogo, wantIcon)
			}
			var got []string
			for _, line := range entryLines(imp.Report.Entries()) {
				if strings.Contains(line, " -> site/settings.json#") {
					got = append(got, line)
				}
			}
			if !slices.Equal(got, tt.wantEntries) {
				t.Errorf("entries about the settings = %q, want %q", got, tt.wantEntries)
			}
		})
	}
}

func TestMediaTitles(t *testing.T) {
	for _, tt := range []struct {
		name   string
		titles []string // of the attachments, in turn; the last one has no file
		want   string
	}{
		{"none", []string{"", " \n\t", ""}, ""},
		{"one", []string{"Photo", "", "Not imported"}, "skipped channel: the title of 1 attachment is not imported; whiteroom media have no titles"},
		{"two", []string{"A", "B", "Not imported"}, "skipped channel: the titles of 2 attachments are not imported; whiteroom media have no titles"},
		{"three", []string{"A", "B", " ", "C", "Not imported"}, "skipped channel: the titles of 3 attachments are not imported; whiteroom media have no titles"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := make(map[string][]byte)
			var items []*wxr.Item
			for i, title := range tt.titles {
				name := fmt.Sprintf("%d.png", i)
				if i < len(tt.titles)-1 {
					files["2026/10/"+name] = testPNG(4+i, 4, 0)
				}
				items = append(items, uploadItem(int64(i+1), name, title))
			}
			imp := newPlannedImport(t, uploadExport("https://example.com", items...))
			runMedia(t, imp, MediaOptions{Files: filesDir(t), Uploads: uploads(files)})
			var got []string
			for _, line := range entryLines(imp.Report.Entries()) {
				if strings.HasPrefix(line, "skipped channel: ") {
					got = append(got, line)
				}
			}
			var want []string
			if tt.want != "" {
				want = []string{tt.want}
			}
			if !slices.Equal(got, want) {
				t.Errorf("entries of the channel = %q, want %q", got, want)
			}
		})
	}
}

func TestMediaTrashAndDraft(t *testing.T) {
	trash, draft, kept := uploadItem(1, "trash.png", ""), uploadItem(2, "draft.png", ""), uploadItem(3, "kept.png", "")
	trash.Status, draft.Status = "trash", "auto-draft"
	imp := newPlannedImport(t, uploadExport("https://example.com", trash, draft, kept))
	// Something else may have given the attachment in the trash an ID.
	id, err := imp.IDs.Assign(Object{Kind: ObjectPost, ID: 1}, content.RefMedia)
	if err != nil {
		t.Fatal(err)
	}
	imp.AddRef(content.RefMedia, id, content.Ref{Type: content.MediaImage})

	data := testPNG(8, 8, 0)
	dir := filesDir(t)
	docs := runMedia(t, imp, MediaOptions{Files: dir, Uploads: uploads(map[string][]byte{
		"2026/10/trash.png": testPNG(9, 9, 0), "2026/10/draft.png": testPNG(10, 10, 0), "2026/10/kept.png": data,
	})})
	if len(docs) != 1 || docs[0].Hash != sha256Hash(data) {
		t.Errorf("documents = %v, want only the attachment that is not in the trash", docs)
	}
	wantFiles(t, dir, data)
	if _, _, ok := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: 1}); ok {
		t.Error("the attachment in the trash is in the table")
	}
	if _, ok := imp.Ref(content.RefMedia, id); ok {
		t.Error("the attachment in the trash has a reference")
	}
	want := []string{
		"skipped item 1: the attachment is not imported: it is in the trash",
		"skipped item 2: the attachment is not imported: it is an auto-draft",
	}
	if got := entryLines(imp.Report.Entries()); !slices.Equal(got, want) {
		t.Errorf("report = %q, want %q", got, want)
	}
}

func TestMediaDownload(t *testing.T) {
	const dirPath = "/wp-content/uploads/2026/10/"
	png, inCopy, pdf := testPNG(24, 12, 3), testPNG(8, 8, 9), testPDF()
	srv, requested := newMediaServer(t, map[string][]byte{dirPath + "a.png": png, dirPath + "d.pdf": pdf})
	newImport := func(t *testing.T) *Import {
		item := func(id int64, name, fileURL string) *wxr.Item {
			it := mediaItem("2026/10/"+name, fileURL, "")
			it.ID, it.Title = id, ""
			return it
		}
		return newPlannedImport(t, uploadExport(srv.URL,
			item(1, "a.png", srv.URL+dirPath+"a.png"),
			item(2, "b.png", srv.URL+dirPath+"b.png"),
			item(3, "c.png", "http://other.example/wp-content/uploads/2026/10/c.png"),
			item(4, "d.pdf", srv.URL+dirPath+"d.pdf"),
		))
	}
	wantReport := []string{
		"skipped item 2: the attachment is not imported: " + fmt.Sprintf(`"b.png": GET %s: 404 Not Found`, srv.URL+dirPath+"b.png"),
		`skipped item 3: the attachment is not imported: "c.png": other.example` + errNotHost,
	}

	t.Run("without a copy", func(t *testing.T) {
		before := len(requested())
		imp := newImport(t)
		dir := filesDir(t)
		docs := runMedia(t, imp, MediaOptions{Files: dir, Download: newTestDownloader(t), Workers: 2})
		if len(docs) != 2 || !slices.ContainsFunc(docs, func(m content.Media) bool { return m.Hash == sha256Hash(png) && m.FileName == "a.png" }) ||
			!slices.ContainsFunc(docs, func(m content.Media) bool { return m.Hash == sha256Hash(pdf) && m.Kind() == content.MediaPDF }) {
			t.Errorf("documents = %v, want the image a.png and the PDF d.pdf", docs)
		}
		wantFiles(t, dir, png, pdf)
		if got := entryLines(imp.Report.Entries()); !slices.Equal(got, wantReport) {
			t.Errorf("report = %q, want %q", got, wantReport)
		}
		got := requested()[before:]
		slices.Sort(got)
		if want := []string{dirPath + "a.png", dirPath + "b.png", dirPath + "d.pdf"}; !slices.Equal(got, want) {
			t.Errorf("requests = %q, want %q: none to another host", got, want)
		}
	})
	t.Run("the copy first", func(t *testing.T) {
		before := len(requested())
		imp := newImport(t)
		dir := filesDir(t)
		docs := runMedia(t, imp, MediaOptions{Files: dir, Uploads: uploads(map[string][]byte{"2026/10/a.png": inCopy}), Download: newTestDownloader(t), Workers: 1})
		if len(docs) != 2 || !slices.ContainsFunc(docs, func(m content.Media) bool { return m.Hash == sha256Hash(inCopy) }) {
			t.Errorf("documents = %v, want the file of the copy for a.png and the downloaded d.pdf", docs)
		}
		wantFiles(t, dir, inCopy, pdf)
		got := requested()[before:]
		slices.Sort(got)
		if want := []string{dirPath + "b.png", dirPath + "d.pdf"}; !slices.Equal(got, want) {
			t.Errorf("requests = %q, want %q: none for the file that the copy has", got, want)
		}
	})
}

// forbidFS fails the test if a file is opened.
type forbidFS struct{ t *testing.T }

func (f forbidFS) Open(name string) (fs.File, error) {
	f.t.Errorf("opened %q", name)
	return nil, fs.ErrNotExist
}

// hookFS calls hook once, when the first file is opened.
type hookFS struct {
	fs.FS
	once sync.Once
	hook func()
}

func (h *hookFS) Open(name string) (fs.File, error) {
	h.once.Do(h.hook)
	return h.FS.Open(name)
}

// closeHookFS calls hook when the file called name is closed, which is after it has been read.
type closeHookFS struct {
	fs.FS
	name string
	hook func()
}

func (c *closeHookFS) Open(name string) (fs.File, error) {
	f, err := c.FS.Open(name)
	if err != nil || name != c.name {
		return f, err
	}
	return closeHookFile{f, c.hook}, nil
}

type closeHookFile struct {
	fs.File
	hook func()
}

func (f closeHookFile) Close() error {
	f.hook()
	return f.File.Close()
}

func TestMediaWrongArguments(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(site *content.Site, opts *MediaOptions)
		want string
	}{
		{"a default language that is not a language code", func(site *content.Site, _ *MediaOptions) { site.DefaultLanguage = "japanese" },
			"the default language of the site: "},
		{"a time zone that is not known", func(site *content.Site, _ *MediaOptions) { site.TimeZone = "Nowhere/Nowhere" }, "the time zone of the site: "},
		// time.LoadLocation reads "" as UTC.
		{"no time zone", func(site *content.Site, _ *MediaOptions) { site.TimeZone = "" }, "the time zone of the site: it is empty"},
		{"an account that is not an ID", func(_ *content.Site, opts *MediaOptions) { opts.By = "admin" }, "the account that uploads the media: "},
		{"an ID of another kind than an account", func(_ *content.Site, opts *MediaOptions) { opts.By = mediaTestID }, "the account that uploads the media: "},
		{"no directory", func(_ *content.Site, opts *MediaOptions) { opts.Files = "" }, "the directory of the originals is needed"},
		{"nowhere to read from", func(_ *content.Site, opts *MediaOptions) { opts.Uploads = nil },
			"a copy of the uploads or a download is needed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := newSampleMediaImport(t, true)
			before := mediaState(imp)
			dir := filesDir(t)
			site, opts := mediaSite(), MediaOptions{Files: dir, Uploads: forbidFS{t}, By: mediaTestBy}
			tt.edit(site, &opts)
			docs, err := imp.Media(context.Background(), site, opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) || docs != nil {
				t.Errorf("Media = %v, %v; want an error with %q", docs, err, tt.want)
			}
			checkUntouched(t, imp, before, dir)
		})
	}
	t.Run("site is nil", func(t *testing.T) {
		imp := newSampleMediaImport(t, true)
		before := mediaState(imp)
		dir := filesDir(t)
		_, err := imp.Media(context.Background(), nil, MediaOptions{Files: dir, Uploads: forbidFS{t}, By: mediaTestBy})
		if err == nil || !strings.Contains(err.Error(), "the settings of the site are needed") {
			t.Errorf("error = %v, want the one for the site that is missing", err)
		}
		checkUntouched(t, imp, before, dir)
	})
}

// checkUntouched checks that nothing is in the report, that the table and the references are as they were, and that
// there is no directory dir.
func checkUntouched(t *testing.T, imp *Import, before, dir string) {
	t.Helper()
	if got := entryLines(imp.Report.Entries()); len(got) != 0 {
		t.Errorf("report = %q, want none", got)
	}
	if got := mediaState(imp); got != before {
		t.Errorf("the table and the references are\n%s\nwant\n%s", got, before)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the directory %s: %v, want it not to exist", dir, err)
	}
}

func TestMediaStops(t *testing.T) {
	files := func() map[string][]byte {
		m := make(map[string][]byte)
		for i := range 6 {
			m[fmt.Sprintf("2026/10/%d.png", i)] = testPNG(4+i, 4, 0)
		}
		return m
	}
	newImport := func(t *testing.T) *Import {
		var items []*wxr.Item
		for i := range 6 {
			items = append(items, uploadItem(int64(i+1), fmt.Sprintf("%d.png", i), "Photo"))
		}
		return newPlannedImport(t, uploadExport("https://example.com", items...))
	}
	// wantStopped checks that the import has stopped without a change to the table, the references and the report.
	wantStopped := func(t *testing.T, imp *Import, before string, err error) {
		t.Helper()
		if got := mediaState(imp); got != before {
			t.Errorf("the table and the references are\n%s\nwant\n%s", got, before)
		}
		if got := entryLines(imp.Report.Entries()); len(got) != 0 {
			t.Errorf("report = %q, want none", got)
		}
		if err == nil {
			t.Error("no error")
		}
	}

	t.Run("the context is done", func(t *testing.T) {
		imp := newImport(t)
		before := mediaState(imp)
		dir := filesDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		docs, err := imp.Media(ctx, mediaSite(), MediaOptions{Files: dir, Uploads: forbidFS{t}, By: mediaTestBy})
		if !errors.Is(err, context.Canceled) || docs != nil {
			t.Errorf("Media = %v, %v; want the error of the context", docs, err)
		}
		checkUntouched(t, imp, before, dir)
	})
	t.Run("the context is done while the files are read", func(t *testing.T) {
		for _, workers := range []int{1, 4} {
			imp := newImport(t)
			before := mediaState(imp)
			ctx, cancel := context.WithCancel(context.Background())
			fsys := &hookFS{FS: uploads(files()), hook: cancel}
			docs, err := imp.Media(ctx, mediaSite(), MediaOptions{Files: filesDir(t), Uploads: fsys, By: mediaTestBy, Workers: workers})
			if !errors.Is(err, context.Canceled) || docs != nil {
				t.Errorf("%d workers: Media = %v, %v; want the error of the context", workers, docs, err)
			}
			wantStopped(t, imp, before, err)
			cancel()
		}
	})
	t.Run("the context is done after the last file", func(t *testing.T) {
		imp := newImport(t)
		before := mediaState(imp)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fsys := &closeHookFS{FS: uploads(files()), name: "2026/10/5.png", hook: cancel}
		docs, err := imp.Media(ctx, mediaSite(), MediaOptions{Files: filesDir(t), Uploads: fsys, By: mediaTestBy, Workers: 1})
		if !errors.Is(err, context.Canceled) || docs != nil {
			t.Errorf("Media = %v, %v; want the error of the context, as not all of the work was done in time", docs, err)
		}
		wantStopped(t, imp, before, err)
	})
	t.Run("the directory is a file", func(t *testing.T) {
		imp := newImport(t)
		before := mediaState(imp)
		dir := filepath.Join(t.TempDir(), "files")
		if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		docs, err := imp.Media(context.Background(), mediaSite(), MediaOptions{Files: dir, Uploads: forbidFS{t}, By: mediaTestBy})
		if err == nil || !strings.HasPrefix(err.Error(), "creating the directory of the originals: ") || docs != nil {
			t.Errorf("Media = %v, %v; want an error that the directory cannot be made", docs, err)
		}
		wantStopped(t, imp, before, err)
	})
	t.Run("the directory is gone", func(t *testing.T) {
		imp := newImport(t)
		before := mediaState(imp)
		dir := filesDir(t)
		fsys := &hookFS{FS: uploads(files()), hook: func() { _ = os.RemoveAll(dir) }}
		docs, err := imp.Media(context.Background(), mediaSite(), MediaOptions{Files: dir, Uploads: fsys, By: mediaTestBy, Workers: 1})
		if err == nil || !strings.HasPrefix(err.Error(), "saving an original in ") || docs != nil {
			t.Errorf("Media = %v, %v; want an error that an original cannot be written", docs, err)
		}
		wantStopped(t, imp, before, err)
	})
	t.Run("the copy cannot be read", func(t *testing.T) {
		imp := newImport(t)
		before := mediaState(imp)
		docs, err := imp.Media(context.Background(), mediaSite(), MediaOptions{Files: filesDir(t), Uploads: errorFS{syscall.EIO}, By: mediaTestBy})
		if err == nil || !strings.HasPrefix(err.Error(), "reading the copy of the uploads: ") || docs != nil {
			t.Errorf("Media = %v, %v; want an error that the copy cannot be read", docs, err)
		}
		wantStopped(t, imp, before, err)
	})
	t.Run("the old site does not answer", func(t *testing.T) {
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			http.Error(w, "down", http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		var items []*wxr.Item
		for i := range 40 {
			it := mediaItem(fmt.Sprintf("2026/10/%d.png", i), fmt.Sprintf("%s/wp-content/uploads/2026/10/%d.png", srv.URL, i), "")
			it.ID = int64(i + 1)
			items = append(items, it)
		}
		imp := newPlannedImport(t, uploadExport(srv.URL, items...))
		before := mediaState(imp)
		d := newTestDownloader(t)
		d.Attempts = 1
		dir := filesDir(t)
		docs, err := imp.Media(context.Background(), mediaSite(), MediaOptions{Files: dir, Download: d, By: mediaTestBy, Workers: 4})
		if err == nil || !strings.HasPrefix(err.Error(), "the old site does not answer: ") || docs != nil {
			t.Errorf("Media = %v, %v; want the error that the old site does not answer", docs, err)
		}
		wantStopped(t, imp, before, err)
		wantNames(t, dir)
		if n := hits.Load(); n < maxDownloadFailures || n > int64(len(items)) {
			t.Errorf("the old site was asked %d times, want %d to %d", n, maxDownloadFailures, len(items))
		}
	})
}

func TestMediaUnreadableMetadata(t *testing.T) {
	data := testPNG(8, 8, 0)
	it := mediaItem("2026/10/a.png", uploadsURL+"2026/10/a.png", "a:1:{broken}")
	it.ID, it.Title = 1, ""
	imp := newPlannedImport(t, uploadExport("https://example.com", it))
	docs := runMedia(t, imp, MediaOptions{Files: filesDir(t), Uploads: uploads(map[string][]byte{"2026/10/a.png": data})})
	if len(docs) != 1 || docs[0].Hash != sha256Hash(data) {
		t.Errorf("documents = %v, want the attached file as it is", docs)
	}
	lines := entryLines(imp.Report.Entries())
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "review item 1 _wp_attachment_metadata: ") ||
		!strings.HasSuffix(lines[0], "; the attached file is imported as it is") {
		t.Errorf("report = %q, want one review of the metadata", lines)
	}
}

func TestMediaWithoutAttachments(t *testing.T) {
	page := &wxr.Item{ID: 10, Type: "page", Status: "publish", Title: "About"}
	for _, e := range []*wxr.Export{uploadExport("https://example.com"), uploadExport("https://example.com", page)} {
		imp := newPlannedImport(t, e)
		before := mediaState(imp)
		dir := filesDir(t)
		docs, err := imp.Media(context.Background(), mediaSite(), MediaOptions{Files: dir, Uploads: forbidFS{t}, By: mediaTestBy})
		if docs != nil || err != nil {
			t.Errorf("Media = %v, %v; want nil and no error", docs, err)
		}
		checkUntouched(t, imp, before, dir)
	}
}
