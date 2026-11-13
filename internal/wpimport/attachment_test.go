package wpimport

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const uploadsURL = "https://example.com/wp-content/uploads/"

// testMedia is a valid medium, uploaded at uploadedAt.
func testMedia(uploadedAt string) content.Media {
	return content.Media{
		ID:         content.FormatID(content.RefMedia, [16]byte{15: 1}),
		Hash:       "sha256:" + strings.Repeat("0", 64),
		FileName:   "a.jpg",
		MIMEType:   "image/jpeg",
		Size:       1,
		Width:      1,
		Height:     1,
		UploadedAt: uploadedAt,
		UploadedBy: content.FormatID(content.RefUser, [16]byte{15: 2}),
	}
}

// attItem is an attachment of the WXR. A postmeta is left out if its value is "".
func attItem(id int64, attached, fileURL, metadata string) *wxr.Item {
	it := &wxr.Item{Pos: wxr.Pos{Line: 40, Column: 3}, ID: id, Type: "attachment", Status: "inherit", Title: "Photo", AttachmentURL: fileURL}
	if attached != "" {
		it.Meta = append(it.Meta, wxr.Meta{Pos: wxr.Pos{Line: 41, Column: 5}, Key: "_wp_attached_file", Value: attached})
	}
	if metadata != "" {
		it.Meta = append(it.Meta, wxr.Meta{Pos: wxr.Pos{Line: 42, Column: 5}, Key: "_wp_attachment_metadata", Value: metadata})
	}
	return it
}

// metaWith is the metadata of an image of WordPress with its original_image.
func metaWith(original string) string {
	return phpArray(phpString("width"), phpInt(1200), phpString("height"), phpInt(800), phpString("original_image"), phpString(original))
}

var plainMeta = phpArray(phpString("width"), phpInt(1200), phpString("height"), phpInt(800), phpString("file"), phpString("2026/10/a.jpg"))

type fileWant struct {
	name, rel, url string
	original       bool
}

func describeFiles(files []mediaFile) []fileWant {
	var got []fileWant
	for _, f := range files {
		w := fileWant{name: f.name, rel: f.rel, original: f.original}
		if f.url != nil {
			w.url = f.url.String()
		}
		got = append(got, w)
	}
	return got
}

func TestAttachmentFiles(t *testing.T) {
	officeScaled := fileWant{"office-scaled.jpg", "2026/10/office-scaled.jpg", uploadsURL + "2026/10/office-scaled.jpg", false}
	office := fileWant{"office.jpg", "2026/10/office.jpg", uploadsURL + "2026/10/office.jpg", true}
	plain := fileWant{"a.jpg", "2026/10/a.jpg", uploadsURL + "2026/10/a.jpg", false}
	officeItem := func(meta string) *wxr.Item {
		return attItem(7, "2026/10/office-scaled.jpg", uploadsURL+"2026/10/office-scaled.jpg", meta)
	}
	aURL := uploadsURL + "2026/10/a.jpg"
	for _, tt := range []struct {
		name   string
		it     *wxr.Item
		want   []fileWant
		edited string
	}{
		{"an image that is not scaled", attItem(1, "2026/10/a.jpg", aURL, plainMeta), []fileWant{plain}, ""},
		{"no metadata", attItem(1, "2026/10/a.jpg", aURL, ""), []fileWant{plain}, ""},
		{"scaled", officeItem(metaWith("office.jpg")), []fileWant{office, officeScaled}, ""},
		{"rotated", attItem(7, "2026/10/office-rotated.jpg", uploadsURL+"2026/10/office-rotated.jpg", metaWith("office.jpg")), []fileWant{
			office, {"office-rotated.jpg", "2026/10/office-rotated.jpg", uploadsURL + "2026/10/office-rotated.jpg", false},
		}, ""},
		{"converted to another format", attItem(8, "2026/10/photo.jpg", uploadsURL+"2026/10/photo.jpg", metaWith("photo.heic")), []fileWant{
			{"photo.heic", "2026/10/photo.heic", uploadsURL + "2026/10/photo.heic", true},
			{"photo.jpg", "2026/10/photo.jpg", uploadsURL + "2026/10/photo.jpg", false},
		}, ""},
		{"edited", attItem(9, "2026/10/team-scaled-e1760000000123.jpg", uploadsURL+"2026/10/team-scaled-e1760000000123.jpg", metaWith("team.jpg")),
			[]fileWant{{"team-scaled-e1760000000123.jpg", "2026/10/team-scaled-e1760000000123.jpg", uploadsURL + "2026/10/team-scaled-e1760000000123.jpg", false}}, "team.jpg"},
		{"edited, without an extension", attItem(9, "2026/10/team-e1760000000123", "", metaWith("team")),
			[]fileWant{{"team-e1760000000123", "2026/10/team-e1760000000123", "", false}}, "team"},
		{"edited, with no original image", attItem(9, "2026/10/team-e1760000000123.jpg", "", plainMeta),
			[]fileWant{{"team-e1760000000123.jpg", "2026/10/team-e1760000000123.jpg", "", false}}, ""},
		{"a name like the mark: 12 digits", attItem(9, "2026/10/team-e176000000012.jpg", "", metaWith("team.jpg")), []fileWant{
			{"team.jpg", "2026/10/team.jpg", "", true}, {"team-e176000000012.jpg", "2026/10/team-e176000000012.jpg", "", false},
		}, ""},
		{"a name like the mark: 14 digits", attItem(9, "2026/10/team-e17600000001234.jpg", "", metaWith("team.jpg")), []fileWant{
			{"team.jpg", "2026/10/team.jpg", "", true}, {"team-e17600000001234.jpg", "2026/10/team-e17600000001234.jpg", "", false},
		}, ""},
		{"a name like the mark: a letter after the digits", attItem(9, "2026/10/team-e1760000000123a.jpg", "", metaWith("team.jpg")), []fileWant{
			{"team.jpg", "2026/10/team.jpg", "", true}, {"team-e1760000000123a.jpg", "2026/10/team-e1760000000123a.jpg", "", false},
		}, ""},
		{"a name like the mark: scaled after it", attItem(9, "2026/10/team-e1760000000123-scaled.jpg", "", metaWith("team.jpg")), []fileWant{
			{"team.jpg", "2026/10/team.jpg", "", true}, {"team-e1760000000123-scaled.jpg", "2026/10/team-e1760000000123-scaled.jpg", "", false},
		}, ""},
		{"a name like the mark: a capital E", attItem(9, "2026/10/team-E1760000000123.jpg", "", metaWith("team.jpg")), []fileWant{
			{"team.jpg", "2026/10/team.jpg", "", true}, {"team-E1760000000123.jpg", "2026/10/team-E1760000000123.jpg", "", false},
		}, ""},
		{"the mark is the name", attItem(9, "-e1760000000123.jpg", "", metaWith("team.jpg")),
			[]fileWant{{"-e1760000000123.jpg", "-e1760000000123.jpg", "", false}}, "team.jpg"},
		{"a file at the top of the uploads", attItem(9, "office-scaled.jpg", uploadsURL+"office-scaled.jpg", metaWith("office.jpg")), []fileWant{
			{"office.jpg", "office.jpg", uploadsURL + "office.jpg", true},
			{"office-scaled.jpg", "office-scaled.jpg", uploadsURL + "office-scaled.jpg", false},
		}, ""},
		{"the first metadata is used", func() *wxr.Item {
			it := attItem(9, "2026/10/a.jpg", "", plainMeta)
			it.Meta = append(it.Meta, wxr.Meta{Key: "_wp_attachment_metadata", Value: metaWith("b.jpg")})
			return it
		}(), []fileWant{{"a.jpg", "2026/10/a.jpg", "", false}}, ""},

		// Only the address, only the path, and neither.
		{"only the address", attItem(2, "", uploadsURL+"2026/10/a.jpg", ""), []fileWant{{"a.jpg", "", aURL, false}}, ""},
		{"only the address, with an original image", attItem(2, "", uploadsURL+"2026/10/office-scaled.jpg", metaWith("office.jpg")), []fileWant{
			{"office.jpg", "", uploadsURL + "2026/10/office.jpg", true}, {"office-scaled.jpg", "", uploadsURL + "2026/10/office-scaled.jpg", false},
		}, ""},
		{"only the path", attItem(2, "2026/10/a.jpg", "", ""), []fileWant{{"a.jpg", "2026/10/a.jpg", "", false}}, ""},
		{"only the path, with an original image", attItem(2, "2026/10/office-scaled.jpg", "", metaWith("office.jpg")), []fileWant{
			{"office.jpg", "2026/10/office.jpg", "", true}, {"office-scaled.jpg", "2026/10/office-scaled.jpg", "", false},
		}, ""},
		{"neither", attItem(2, "", "", plainMeta), nil, ""},
		{"neither, with an original image", attItem(2, "", "", metaWith("office.jpg")), nil, ""},

		// The path.
		{"a slash at the start", attItem(3, "/2026/10/a.jpg", "", ""), []fileWant{{"a.jpg", "2026/10/a.jpg", "", false}}, ""},
		{"space around", attItem(3, " \n2026/10/a.jpg\t", "", ""), []fileWant{{"a.jpg", "2026/10/a.jpg", "", false}}, ""},
		{"slashes in a row", attItem(3, "2026//10/./a.jpg", "", ""), []fileWant{{"a.jpg", "2026/10/a.jpg", "", false}}, ""},
		{"out of the uploads", attItem(3, "../x.jpg", "", ""), nil, ""},
		{"out of the uploads, with the address", attItem(3, "../x.jpg", uploadsURL+"2026/10/a.jpg", ""), []fileWant{{"a.jpg", "", aURL, false}}, ""},
		{"out of the uploads, in the middle", attItem(3, "2026/../../x.jpg", "", ""), nil, ""},
		{"a backslash", attItem(3, `2026\10\a.jpg`, uploadsURL+"2026/10/a.jpg", ""), []fileWant{{"a.jpg", "", aURL, false}}, ""},
		{"a query", attItem(3, "2026/10/a.jpg?x=1", "", ""), nil, ""},
		{"a fragment", attItem(3, "2026/10/a.jpg#x", "", ""), nil, ""},
		{"a NUL", attItem(3, "2026/10/a\x00.jpg", "", ""), nil, ""},
		{"only slashes", attItem(3, "///", "", ""), nil, ""},
		{"a dot", attItem(3, ".", "", ""), nil, ""},
		{"a directory", attItem(3, "2026/10/", "", ""), []fileWant{{"10", "2026/10", "", false}}, ""},

		// The address.
		{"an address with a fragment", attItem(4, "", aURL+"#top", ""), []fileWant{{"a.jpg", "", aURL, false}}, ""},
		{"an address with space around", attItem(4, "", "\n "+aURL+" \n", ""), []fileWant{{"a.jpg", "", aURL, false}}, ""},
		{"an address with a query", attItem(4, "", aURL+"?v=2", ""), []fileWant{{"a.jpg", "", aURL + "?v=2", false}}, ""},
		{"an address with a port", attItem(4, "", "http://example.com:8080/a.jpg", ""), []fileWant{{"a.jpg", "", "http://example.com:8080/a.jpg", false}}, ""},
		{"an address with a user", attItem(4, "", "https://user:pass@example.com/a.jpg", ""), nil, ""},
		{"an address with an empty user", attItem(4, "", "https://@example.com/a.jpg", ""), nil, ""},
		{"an FTP address", attItem(4, "", "ftp://example.com/a.jpg", ""), nil, ""},
		{"an address with no scheme", attItem(4, "", "//example.com/a.jpg", ""), nil, ""},
		{"a path", attItem(4, "", "/wp-content/uploads/a.jpg", ""), nil, ""},
		{"an address with no host", attItem(4, "", "https:///a.jpg", ""), nil, ""},
		{"an address with a port only", attItem(4, "", "https://:8080/a.jpg", ""), nil, ""},
		{"an opaque address", attItem(4, "", "https:a.jpg", ""), nil, ""},
		{"an address that is not one", attItem(4, "", "https://[::1/a.jpg", ""), nil, ""},
		{"an address with a control character", attItem(4, "", "https://example.com/a\x01.jpg", ""), nil, ""},
		{"an address with a slash at the end", attItem(4, "", "https://example.com/files/", ""), nil, ""},
		{"an address with no path", attItem(4, "", "https://example.com", ""), nil, ""},
		{"an address of a dot", attItem(4, "", "https://example.com/files/..", ""), nil, ""},
		{"a path wins over the address for the name", attItem(4, "2026/10/b.jpg", aURL, ""), []fileWant{{"b.jpg", "2026/10/b.jpg", aURL, false}}, ""},

		// Names that are not ASCII, and the original image that has one.
		{"a name that is written as it is", attItem(5, "2026/10/写真.jpg", uploadsURL+"2026/10/写真.jpg", ""),
			[]fileWant{{"写真.jpg", "2026/10/写真.jpg", uploadsURL + "2026/10/%E5%86%99%E7%9C%9F.jpg", false}}, ""},
		{"a name that is percent-encoded", attItem(5, "", uploadsURL+"2026/10/%E5%86%99%E7%9C%9F.jpg", ""),
			[]fileWant{{"写真.jpg", "", uploadsURL + "2026/10/%E5%86%99%E7%9C%9F.jpg", false}}, ""},
		{"a name that is not ASCII, written as it is in the address", attItem(5, "", uploadsURL+"2026/10/写真.jpg", ""),
			[]fileWant{{"写真.jpg", "", uploadsURL + "2026/10/%E5%86%99%E7%9C%9F.jpg", false}}, ""},
		{"a slash that is percent-encoded", attItem(5, "", uploadsURL+"2026/10/a%2Fb.jpg", ""), []fileWant{{"b.jpg", "", uploadsURL + "2026/10/a%2Fb.jpg", false}}, ""},
		{"an original image with a name that is not ASCII", attItem(5, "2026/10/写真-scaled.jpg", uploadsURL+"2026/10/%E5%86%99%E7%9C%9F-scaled.jpg", metaWith("写真 1.jpg")), []fileWant{
			{"写真 1.jpg", "2026/10/写真 1.jpg", uploadsURL + "2026/10/%E5%86%99%E7%9C%9F%201.jpg", true},
			{"写真-scaled.jpg", "2026/10/写真-scaled.jpg", uploadsURL + "2026/10/%E5%86%99%E7%9C%9F-scaled.jpg", false},
		}, ""},
		{"an original image next to an address with a slash at the end", attItem(5, "2026/10/office-scaled.jpg", "https://example.com/files/", metaWith("office.jpg")), []fileWant{
			{"office.jpg", "2026/10/office.jpg", "https://example.com/files/office.jpg", true},
			{"office-scaled.jpg", "2026/10/office-scaled.jpg", "https://example.com/files/", false},
		}, ""},

		{"an original image next to an address with no path", attItem(5, "2026/10/office-scaled.jpg", "https://example.com", metaWith("office.jpg")), []fileWant{
			{"office.jpg", "2026/10/office.jpg", "https://example.com/office.jpg", true},
			{"office-scaled.jpg", "2026/10/office-scaled.jpg", "https://example.com", false},
		}, ""},

		// original_image that is not used.
		{"an original image that goes up", officeItem(metaWith("../x.jpg")), []fileWant{officeScaled}, ""},
		{"an original image with a slash", officeItem(metaWith("a/b.jpg")), []fileWant{officeScaled}, ""},
		{"an original image with a backslash", officeItem(metaWith(`a\b.jpg`)), []fileWant{officeScaled}, ""},
		{"an original image with a NUL", officeItem(metaWith("a\x00b.jpg")), []fileWant{officeScaled}, ""},
		{"an original image of two dots", officeItem(metaWith("..")), []fileWant{officeScaled}, ""},
		{"an original image of a dot", officeItem(metaWith(".")), []fileWant{officeScaled}, ""},
		{"an original image that is empty", officeItem(metaWith("")), []fileWant{officeScaled}, ""},
		{"an original image that is the attached file", officeItem(metaWith("office-scaled.jpg")), []fileWant{officeScaled}, ""},
		{"an original image that is not a string", officeItem(phpArray(phpString("original_image"), phpInt(5))), []fileWant{officeScaled}, ""},
		{"an original image that is an array", officeItem(phpArray(phpString("original_image"), phpArray(phpString("a"), phpString("b")))), []fileWant{officeScaled}, ""},

		// Metadata that is not an array is nothing to read from, and is not a problem.
		{"metadata that is not serialized", officeItem("office.jpg"), []fileWant{officeScaled}, ""},
		{"metadata that is a string", officeItem(phpString("office.jpg")), []fileWant{officeScaled}, ""},
		{"metadata that is an integer", officeItem(phpInt(5)), []fileWant{officeScaled}, ""},
		{"metadata that is empty", officeItem(phpArray()), []fileWant{officeScaled}, ""},
		{"metadata that is space", officeItem(" \n\t"), []fileWant{officeScaled}, ""},
		{"metadata that only starts like a serialized value", officeItem("a:1:{broken"), []fileWant{officeScaled}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := New("wordpress.xml", &wxr.Export{}, NewReport(Info{File: "wordpress.xml"}))
			files, edited, note := imp.attachmentFiles(tt.it)
			if got := describeFiles(files); !slices.Equal(got, tt.want) {
				t.Errorf("attachmentFiles = %+v, want %+v", got, tt.want)
			}
			for _, f := range files {
				if f.original && f.url != nil && (!strings.HasPrefix(f.url.Path, "/") || f.url.RawPath != "") {
					t.Errorf("the address of the original image has the path %q and the raw path %q", f.url.Path, f.url.RawPath)
				}
			}
			if edited != tt.edited {
				t.Errorf("editedFrom = %q, want %q", edited, tt.edited)
			}
			if note != nil {
				t.Errorf("note = %+v, want none", note)
			}
			if got := imp.Report.Entries(); len(got) != 0 {
				t.Errorf("the report has %+v, want nothing", got)
			}
			if name := attachedName(tt.it); (len(files) == 0) != (name == "") || (len(files) > 0 && name != files[len(files)-1].name) {
				t.Errorf("attachedName = %q for the files %+v", name, files)
			}
		})
	}
}

func TestAttachmentFilesMetadataThatCannotBeRead(t *testing.T) {
	for _, tt := range []struct {
		name, meta string
		wantMsg    string
	}{
		{"a broken array", "a:1:{broken}", `the value of "_wp_attachment_metadata": byte `},
		{"a length that is wrong", `a:1:{s:3:"original_image";s:99:"office.jpg";}`, `the value of "_wp_attachment_metadata": byte `},
		{"an array with fewer entries than it counts", `a:2:{s:5:"width";i:1200;}`, `the value of "_wp_attachment_metadata": byte `},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := New("wordpress.xml", &wxr.Export{}, NewReport(Info{File: "wordpress.xml"}))
			it := attItem(7, "2026/10/office-scaled.jpg", uploadsURL+"2026/10/office-scaled.jpg", tt.meta)
			files, edited, note := imp.attachmentFiles(it)
			if got, want := describeFiles(files), []fileWant{{"office-scaled.jpg", "2026/10/office-scaled.jpg", uploadsURL + "2026/10/office-scaled.jpg", false}}; !slices.Equal(got, want) {
				t.Errorf("attachmentFiles = %+v, want %+v", got, want)
			}
			if edited != "" {
				t.Errorf("editedFrom = %q, want none", edited)
			}
			if note == nil {
				t.Fatal("no note")
			}
			if note.Kind != Review {
				t.Errorf("the note is %q, want %q", note.Kind, Review)
			}
			wantSource := Source{File: "wordpress.xml", Line: 42, Column: 5, Record: recordItem, ID: 7, Type: "attachment", Name: "Photo", Meta: "_wp_attachment_metadata"}
			if note.Source != wantSource {
				t.Errorf("the source is %+v, want %+v", note.Source, wantSource)
			}
			if !strings.HasPrefix(note.Message, tt.wantMsg) || !strings.HasSuffix(note.Message, "; the attached file is imported as it is") {
				t.Errorf("the message is %q", note.Message)
			}
			if note.Target != (Target{}) {
				t.Errorf("the target is %+v, want none", note.Target)
			}
			if got := imp.Report.Entries(); len(got) != 0 {
				t.Errorf("the report has %+v, want nothing", got)
			}
		})
	}
}

func TestAttachmentFilesAnEditedImageWhoseMetadataCannotBeRead(t *testing.T) {
	imp := New("wordpress.xml", &wxr.Export{}, NewReport(Info{File: "wordpress.xml"}))
	files, edited, note := imp.attachmentFiles(attItem(7, "2026/10/team-e1760000000123.jpg", "", "a:1:{broken}"))
	if len(files) != 1 || files[0].original || edited != "" || note == nil {
		t.Errorf("attachmentFiles = %+v, %q, %+v", files, edited, note)
	}
}

func TestAttachedName(t *testing.T) {
	for _, tt := range []struct {
		it   *wxr.Item
		want string
	}{
		{attItem(1, "2026/10/a.jpg", "", ""), "a.jpg"},
		{attItem(1, "", uploadsURL+"2026/10/b.png", ""), "b.png"},
		{attItem(1, "", "", plainMeta), ""},
		{&wxr.Item{ID: 1, Type: "attachment"}, ""},
	} {
		if got := attachedName(tt.it); got != tt.want {
			t.Errorf("attachedName(%+v) = %q, want %q", tt.it, got, tt.want)
		}
	}
}

func TestGuessMediaKind(t *testing.T) {
	for name, want := range map[string]string{
		"a.pdf": content.MediaPDF, "a.PDF": content.MediaPDF, "a.Pdf": content.MediaPDF, ".pdf": content.MediaPDF,
		"レポート.pdf": content.MediaPDF, "a.b.pdf": content.MediaPDF,
		"a.jpg": content.MediaImage, "a": content.MediaImage, "pdf": content.MediaImage, "a.pdf.jpg": content.MediaImage,
		"a.pdfx": content.MediaImage, "": content.MediaImage, "a.": content.MediaImage,
	} {
		if got := guessMediaKind(name); got != want {
			t.Errorf("guessMediaKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestImportsAttachment(t *testing.T) {
	for _, tt := range []struct {
		typ, status string
		want        bool
	}{
		{"attachment", "inherit", true},
		{"attachment", "publish", true},
		{"attachment", "private", true},
		{"attachment", "draft", true},
		{"attachment", "", true},
		{"attachment", "trash", false},
		{"attachment", "auto-draft", false},
		{"post", "publish", false},
		{"page", "inherit", false},
		{"", "inherit", false},
	} {
		if got := importsAttachment(&wxr.Item{Type: tt.typ, Status: tt.status}); got != tt.want {
			t.Errorf("importsAttachment(%s, %s) = %v, want %v", tt.typ, tt.status, got, tt.want)
		}
	}
}

// checkFileName fails the test unless name is accepted as the fileName of a medium and fixFileName leaves it as it is.
func checkFileName(t *testing.T, name string) {
	t.Helper()
	m := testMedia("2026-10-05T09:45:00+09:00")
	m.FileName = name
	if err := m.Validate(termSite()); err != nil {
		var errs content.Errors
		if !errors.As(err, &errs) {
			t.Fatalf("Validate of the file name %q = %v", name, err)
		}
		for _, e := range errs {
			if e.Path == "/fileName" {
				t.Errorf("the file name %q is not valid: %v", name, e)
			}
		}
	}
	if again, why := fixFileName(name); again != name || why != nil {
		t.Errorf("fixFileName(%q) = %q, %q; want it as it is", name, again, why)
	}
}

func TestFixFileName(t *testing.T) {
	const (
		badBytes = `bytes that are not UTF-8 are "-"`
		slashes  = `"/" and "\" are "-"`
		bidi     = "bidirectional formatting characters are removed"
		empty    = "nothing is left of it"
		cut      = "it is cut to 255 bytes"
	)
	r := func(c rune) string { return string(c) }
	long := strings.Repeat("あ", 100)
	for _, tt := range []struct {
		name, in, want string
		why            []string
	}{
		{"an ASCII name", "photo.jpg", "photo.jpg", nil},
		{"a Japanese name", "写真 2026.jpg", "写真 2026.jpg", nil},
		{"an emoji", "🙂.png", "🙂.png", nil},
		{"no extension", "README", "README", nil},
		{"a name of dots and spaces", ". .", ". .", nil},
		{"a name that starts with a dot", ".htaccess", ".htaccess", nil},
		{"NFC", "e" + r(0x301) + ".jpg", "é.jpg", nil},
		{"NFC of Hangul", r(0x1100) + r(0x1161) + ".png", "가.png", nil},
		{"control characters", "a\x00b\tc\nd" + r(0x85) + "e.jpg", "abcde.jpg", nil},
		{"space around", " \t a.jpg \n", "a.jpg", nil},
		{"space of other kinds around", r(0xA0) + r(0x3000) + "a.jpg" + r(0x2003), "a.jpg", nil},

		{"a byte that is not UTF-8", "a\xffb.jpg", "a-b.jpg", []string{badBytes}},
		{"bytes in a row", "a\xff\xfeb.jpg", "a--b.jpg", []string{badBytes}},
		{"half of a character", "a\xe3\x81.jpg", "a--.jpg", []string{badBytes}},
		{"a character that is in the name for the replacement", "a" + r(0xFFFD) + "\xff.jpg", "a" + r(0xFFFD) + "-.jpg", []string{badBytes}},
		{"a slash", "a/b.jpg", "a-b.jpg", []string{slashes}},
		{"a backslash", `a\b.jpg`, "a-b.jpg", []string{slashes}},
		{"both", `..\..//a.jpg`, "..-..--a.jpg", []string{slashes}},
		{"a path", "2026/10/a.jpg", "2026-10-a.jpg", []string{slashes}},
		{"the right-to-left override", "photo" + r(0x202E) + "gpj.exe", "photogpj.exe", []string{bidi}},
		{"other bidirectional characters", r(0x200E) + "a" + r(0x2066) + "b" + r(0x2069) + r(0x061C) + r(0x200F) + "c.jpg", "abc.jpg", []string{bidi}},
		{"all three", "a\xff/" + r(0x202E) + "b.jpg", "a--b.jpg", []string{badBytes, slashes, bidi}},
		{"characters that join when one is removed", "e" + r(0x202E) + r(0x301) + ".jpg", "é.jpg", []string{bidi}},
		{"a space that is left at the end by a removal", "a " + r(0x202E), "a", []string{bidi}},
		{"a control character in a name of dots", ".\x00.", "file", []string{empty}},

		{"empty", "", "file", []string{empty}},
		{"spaces", " \t ", "file", []string{empty}},
		{"a dot", ".", "file", []string{empty}},
		{"two dots", "..", "file", []string{empty}},
		{"two dots with a bidirectional character", "." + r(0x202E) + ".", "file", []string{bidi, empty}},
		{"only a slash", "/", "-", []string{slashes}},
		{"only a control character", "\x00", "file", []string{empty}},
		{"three dots", "...", "...", nil},

		{"too long", strings.Repeat("a", 300) + ".jpg", strings.Repeat("a", 251) + ".jpg", []string{cut}},
		{"too long, in characters of 3 bytes", long + ".jpg", strings.Repeat("あ", 83) + ".jpg", []string{cut}},
		{"too long, with no extension", long + long, strings.Repeat("あ", 85), []string{cut}},
		{"too long, in characters of 4 bytes", strings.Repeat("🙂", 70) + ".png", strings.Repeat("🙂", 62) + ".png", []string{cut}},
		{"too long, with an extension of 32 bytes", strings.Repeat("a", 300) + "." + strings.Repeat("b", 31),
			strings.Repeat("a", 223) + "." + strings.Repeat("b", 31), []string{cut}},
		{"too long, with an extension of 33 bytes", strings.Repeat("a", 300) + "." + strings.Repeat("b", 32), strings.Repeat("a", 255), []string{cut}},
		{"too long, with a dot at the end", strings.Repeat("a", 300) + ".", strings.Repeat("a", 255), []string{cut}},
		{"too long, with only a dot at the start", "." + strings.Repeat("a", 300), "." + strings.Repeat("a", 254), []string{cut}},
		{"too long, with spaces where it is cut", "a" + strings.Repeat(" ", 300) + "b.txt", "a.txt", []string{cut}},
		{"too long, of spaces after a dot", "." + strings.Repeat(" ", 300) + "x", "file", []string{cut}},
		{"too long, of dots", strings.Repeat(".", 300), strings.Repeat(".", 255), []string{cut}},
		{"too long, of spaces and dots", ". " + strings.Repeat(" ", 300) + ".b", "..b", []string{cut}},
		{"too long before the removals", strings.Repeat("\x00", 300) + "a.jpg", "a.jpg", nil},
		{"exactly 255 bytes", strings.Repeat("a", 251) + ".jpg", strings.Repeat("a", 251) + ".jpg", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, why := fixFileName(tt.in)
			if got != tt.want || !slices.Equal(why, tt.why) {
				t.Fatalf("fixFileName(%q) = %q, %q; want %q, %q", tt.in, got, why, tt.want, tt.why)
			}
			if !utf8.ValidString(got) || len(got) > content.MaxFileNameBytes {
				t.Errorf("the file name %q is not valid UTF-8 or is too long: %d bytes", got, len(got))
			}
			checkFileName(t, got)
		})
	}
}

func FuzzFixFileName(f *testing.F) {
	for _, s := range []string{
		"", ".", "..", "...", "photo.jpg", "写真.jpg", " a ", "a/b", `a\b`, "a\xffb", "a\x00b", "a\nb", "e" + string(rune(0x301)),
		"a" + string(rune(0x202E)) + "b", "." + string(rune(0x202E)) + ".", string(rune(0xA0)),
		strings.Repeat("a", 300) + ".jpg", strings.Repeat("あ", 100), "." + strings.Repeat(" ", 300) + "x",
		strings.Repeat("a", 254) + "あ", string(rune(0x1100)) + string(rune(0x1161)) + strings.Repeat(string(rune(0x11A8)), 100), strings.Repeat("e"+string(rune(0x301)), 200),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got, _ := fixFileName(name)
		checkFileName(t, got)
	})
}

func TestPlanMedia(t *testing.T) {
	trash := attItem(14, "2026/10/trash.jpg", "", "")
	trash.Status = "trash"
	draft := attItem(15, "2026/10/draft.jpg", "", "")
	draft.Status = "auto-draft"
	post := &wxr.Item{ID: 16, Type: "post", Status: "publish", Title: "A post"}
	e := &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items: []wxr.Item{
			*attItem(10, "2026/10/a.jpg", "", ""),
			*attItem(11, "2026/10/doc.pdf", "", ""),
			*attItem(12, "", uploadsURL+"2026/10/DOC.PDF", ""),
			*attItem(13, "2026/10/noext", "", ""),
			*trash, *draft, *post,
			*attItem(17, "", "", ""),
			*attItem(18, "2026/10/report.pdf.png", "", ""),
		},
	}
	imp := New("wordpress.xml", e, NewReport(Info{File: "wordpress.xml"}))
	if err := imp.PlanMedia(); err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{
		10: content.MediaImage, 11: content.MediaPDF, 12: content.MediaPDF, 13: content.MediaImage, 17: content.MediaImage, 18: content.MediaImage,
	}
	ids := make(map[int64]string)
	check := func() {
		t.Helper()
		for i := range e.Items {
			it := &e.Items[i]
			kind, id, ok := imp.IDs.Lookup(PostObject(it))
			wantKind, planned := want[it.ID]
			if ok != planned {
				t.Errorf("Lookup of post %d is %v, want %v", it.ID, ok, planned)
				continue
			}
			if !planned {
				continue
			}
			if kind != content.RefMedia || id != newID(content.RefMedia, "https://example.com", PostObject(it).key()) {
				t.Errorf("Lookup of post %d = %s, %s", it.ID, kind, id)
			}
			if old, ok := ids[it.ID]; ok && old != id {
				t.Errorf("the ID of post %d is %s, and was %s", it.ID, id, old)
			}
			ids[it.ID] = id
			ref, ok := imp.Ref(content.RefMedia, id)
			if !ok || ref != (content.Ref{Type: wantKind}) {
				t.Errorf("Ref of post %d = %+v, %v; want the kind %q", it.ID, ref, ok, wantKind)
			}
			if _, ok := imp.Ref(content.RefEntry, id); ok {
				t.Errorf("Ref of post %d is found as an entry", it.ID)
			}
		}
		if len(imp.refs) != len(want) {
			t.Errorf("%d records, want %d", len(imp.refs), len(want))
		}
		if got := imp.Report.Entries(); len(got) != 0 {
			t.Errorf("the report has %+v, want nothing", got)
		}
	}
	check()
	if err := imp.PlanMedia(); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestPlanMediaOfAPostThatIsAnEntry(t *testing.T) {
	e := &wxr.Export{Channel: wxr.Channel{Link: "https://example.com"}, Items: []wxr.Item{*attItem(10, "2026/10/a.jpg", "", "")}}
	imp := New("wordpress.xml", e, NewReport(Info{File: "wordpress.xml"}))
	if _, err := imp.IDs.Assign(PostObject(&e.Items[0]), content.RefEntry); err != nil {
		t.Fatal(err)
	}
	err := imp.PlanMedia()
	if err == nil || !strings.Contains(err.Error(), "post 10 is already imported as entry") {
		t.Errorf("PlanMedia = %v, want the error that the post is an entry", err)
	}
	if len(imp.refs) != 0 {
		t.Errorf("%d records, want none", len(imp.refs))
	}
}

func TestPlanMediaOfNothing(t *testing.T) {
	imp := New("wordpress.xml", &wxr.Export{}, NewReport(Info{}))
	if err := imp.PlanMedia(); err != nil {
		t.Error(err)
	}
	if len(imp.refs) != 0 {
		t.Errorf("%d records, want none", len(imp.refs))
	}
}
