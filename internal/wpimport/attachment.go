package wpimport

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/phpserialize"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// mediaFile is a file that a medium may be imported from.
type mediaFile struct {
	name     string   // the name of the file, which the fileName of the medium comes from
	rel      string   // the path in the uploads directory, such as "2026/10/a.jpg", or "" if it is not known
	url      *url.URL // the address on the old site, or nil
	original bool     // the file that original_image of the metadata names
}

// importsAttachment reports whether it is an attachment that is imported: not one in the trash, nor an automatic draft.
func importsAttachment(it *wxr.Item) bool {
	return it.Type == "attachment" && it.Status != "trash" && it.Status != "auto-draft"
}

// attachedMediaFile returns the file that the attachment itself is, from _wp_attached_file and wp:attachment_url, or the
// zero value if neither tells a name.
func attachedMediaFile(it *wxr.Item) mediaFile {
	f := mediaFile{rel: attachedRel(it), url: attachedURL(it)}
	switch {
	case f.rel != "":
		f.name = path.Base(f.rel)
	case f.url != nil:
		f.name = lastSegment(f.url.Path)
	}
	if f.name == "" {
		return mediaFile{}
	}
	return f
}

// attachedRel returns _wp_attached_file of the attachment as a path in the uploads directory, or "" if it is not one.
func attachedRel(it *wxr.Item) string {
	v, _ := it.Meta.Get("_wp_attached_file")
	v = strings.TrimLeft(strings.TrimSpace(v), "/")
	if v == "" || strings.ContainsAny(v, "\x00?#\\") {
		return ""
	}
	v = path.Clean(v)
	if v == "." || !fs.ValidPath(v) {
		return ""
	}
	return v
}

// attachedURL returns wp:attachment_url of the attachment, or nil if it is not the address of a file on a web server.
func attachedURL(it *wxr.Item) *url.URL {
	u, err := url.Parse(strings.TrimSpace(it.AttachmentURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil
	}
	u.Fragment, u.RawFragment = "", ""
	return u
}

// lastSegment returns what follows the last slash of p, or "" if that is nothing, or "." or "..".
func lastSegment(p string) string {
	s := p[strings.LastIndexByte(p, '/')+1:]
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// attachedName returns the name of the file of the attachment, or "" if there is none.
func attachedName(it *wxr.Item) string {
	return attachedMediaFile(it).name
}

// guessMediaKind returns the kind that the medium of a file called name is likely to be.
func guessMediaKind(name string) string {
	if strings.EqualFold(path.Ext(name), ".pdf") {
		return content.MediaPDF
	}
	return content.MediaImage
}

// attachmentFiles returns the files to try to import the attachment from, in the order to try them: the original image
// that WordPress kept when it scaled or rotated the image or converted it to another format, if _wp_attachment_metadata
// names one, then the attached file. If the attached file is one that WordPress saved when someone edited the image, the
// original image is not one to try, and editedFrom is its name. There are no files if the attachment has no name for its
// file. note is for the report, if the metadata cannot be read; the caller adds it.
func (imp *Import) attachmentFiles(it *wxr.Item) (files []mediaFile, editedFrom string, note *Entry) {
	att := attachedMediaFile(it)
	if att.name == "" {
		return nil, "", nil
	}
	original := ""
	for _, m := range it.Meta {
		if m.Key != "_wp_attachment_metadata" {
			continue
		}
		if strings.TrimSpace(m.Value) == "" {
			break
		}
		src := ItemSource(imp.File, it)
		v, err := Unserialize(nil, src, m)
		if err != nil {
			msg := err.Error()
			var ce *content.Error
			if errors.As(err, &ce) {
				msg = ce.Msg
			}
			note = &Entry{Kind: Review, Source: MetaSource(src, m), Message: msg + "; the attached file is imported as it is"}
		} else if o, ok := v.Lookup("original_image"); ok && o.Kind == phpserialize.String && usableOriginal(o.Str, att.name) {
			original = o.Str
		}
		break
	}
	if original == "" {
		return []mediaFile{att}, "", note
	}
	if hasEditMark(att.name) {
		return []mediaFile{att}, original, note
	}
	orig := mediaFile{name: original, original: true}
	if att.rel != "" {
		orig.rel = path.Join(path.Dir(att.rel), original)
	}
	if att.url != nil {
		u := *att.url
		u.Path = u.Path[:strings.LastIndexByte(u.Path, '/')+1] + original
		if !strings.HasPrefix(u.Path, "/") {
			u.Path = "/" + u.Path
		}
		u.RawPath = ""
		orig.url = &u
	}
	return []mediaFile{orig, att}, "", note
}

// usableOriginal reports whether name, the original_image of an attachment whose file is called attached, is a name of a
// file next to the attached one.
func usableOriginal(name, attached string) bool {
	return name != "" && name != "." && name != ".." && name != attached && !strings.ContainsAny(name, "/\\\x00")
}

// hasEditMark reports whether the name of a file ends, before its extension, with "-e" and 13 digits, as the name of a
// file that WordPress saves when someone edits an image does.
func hasEditMark(name string) bool {
	stem := strings.TrimSuffix(name, path.Ext(name))
	if len(stem) < len("-e")+13 {
		return false
	}
	tail := stem[len(stem)-len("-e")-13:]
	return strings.HasPrefix(tail, "-e") && isDigits(tail[len("-e"):])
}

// fixFileName returns name as the fileName of a medium: what content.Media.Validate accepts, and what fixFileName does not
// change again. It also returns what it changed, as the phrases of a report, other than the normalization of content.
func fixFileName(name string) (string, []string) {
	var why []string
	s := name
	if !utf8.ValidString(s) {
		var b strings.Builder
		for i := 0; i < len(s); {
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				b.WriteByte('-')
			} else {
				b.WriteString(s[i : i+n])
			}
			i += n
		}
		s = b.String()
		why = append(why, `bytes that are not UTF-8 are "-"`)
	}
	if strings.ContainsAny(s, `/\`) {
		s = strings.NewReplacer("/", "-", `\`, "-").Replace(s)
		why = append(why, `"/" and "\" are "-"`)
	}
	if stripped := strings.Map(dropBidi, s); stripped != s {
		s = stripped
		why = append(why, "bidirectional formatting characters are removed")
	}
	// After the removals, which can join characters that were apart.
	m := content.Media{FileName: s}
	m.Normalize()
	s = m.FileName
	if s == "" || s == "." || s == ".." {
		s = "file"
		why = append(why, "nothing is left of it")
	}
	if len(s) > content.MaxFileNameBytes {
		s = cutFileName(s)
		why = append(why, fmt.Sprintf("it is cut to %d bytes", content.MaxFileNameBytes))
	}
	return s, why
}

func dropBidi(r rune) rune {
	if unicode.Is(unicode.Bidi_Control, r) {
		return -1
	}
	return r
}

// cutFileName cuts s, a name that is too long, to content.MaxFileNameBytes, keeping its extension if it has a short one.
func cutFileName(s string) string {
	ext := ""
	if i := strings.LastIndexByte(s, '.'); i >= 0 && len(s)-i >= 2 && len(s)-i <= 32 {
		ext = s[i:]
	}
	stem := s[:len(s)-len(ext)]
	n := content.MaxFileNameBytes - len(ext)
	for n > 0 && !utf8.RuneStart(stem[n]) {
		n--
	}
	out := strings.TrimRightFunc(stem[:n], unicode.IsSpace) + ext
	if out == "" || out == "." || out == ".." {
		return "file"
	}
	return out
}

// PlanMedia is the part of the plan (6.10.5) that is for the media: it gives the ID of a medium to each attachment that
// is imported, and records it with the kind that the name of its file tells, as AddRef does. Call it before Site, which
// finds the logo and the icon among the records. The kind is a guess: the conversion of the media corrects it from the
// content of the file, and drops from the table what it cannot import.
func (imp *Import) PlanMedia() error {
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if !importsAttachment(it) {
			continue
		}
		id, err := imp.IDs.Assign(PostObject(it), content.RefMedia)
		if err != nil {
			return err
		}
		imp.AddRef(content.RefMedia, id, content.Ref{Type: guessMediaKind(attachedName(it))})
	}
	return nil
}
