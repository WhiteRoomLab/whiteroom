package wpimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/imaging"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// MediaOptions is what the conversion of the media (6.10.5) needs besides the WXR.
type MediaOptions struct {
	// Files is the directory that the originals are written to: files/ of the site directory.
	Files string
	// Uploads is a copy of wp-content/uploads of the old site, or nil.
	Uploads fs.FS
	// Download reads from the old site the files that Uploads does not have, or is nil.
	Download *Downloader
	// By is the ID of the account that the media are uploaded by.
	By string
	// Workers is the number of attachments that are read at once.
	Workers int
}

// mediaConv makes the media of the attachments of the WXR. Its methods may be called concurrently: it changes nothing of
// imp, which it only reads.
type mediaConv struct {
	imp   *Import
	site  *content.Site
	opts  MediaOptions
	files *originals
	home  *url.URL       // the home page of the old site, or nil
	zone  *time.Location // the time zone of the site
}

// mediaResult is what convert makes of an attachment: the medium, which is nil if the attachment is not imported, and what
// the report says about it.
type mediaResult struct {
	media   *content.Media
	entries []Entry
}

// candidate is a file that a medium is made from, or the reason why the file is not used.
type candidate struct {
	media   *content.Media
	orig    *original // the temporary file, until convert keeps it
	file    mediaFile
	entries []Entry // about the medium, which are the report's only if the candidate is the one that is imported
	why     string  // if the file is not used, and then nothing else is set
}

// notUsed is a file of an attachment that was not used, with the reason.
type notUsed struct{ name, why string }

// convert makes the medium of the attachment it, which has the ID id, from the first of files that can be read and that
// makes a valid medium, and writes the original to the directory of the originals. editedFrom is what attachmentFiles
// returns. The error is only for what stops the import: ctx is done, a file cannot be written, the old site does not
// answer. Anything wrong with an attachment is in the entries, which have the kind skipped if it is not imported.
func (c *mediaConv) convert(ctx context.Context, it *wxr.Item, id string, files []mediaFile, editedFrom string) (mediaResult, error) {
	if err := ctx.Err(); err != nil {
		return mediaResult{}, err
	}
	uploadedAt, ok := postTime(it.Date, it.DateGMT, c.zone)
	if !ok {
		return c.notImported(it, fmt.Sprintf("its post_date %q cannot be read", it.Date)), nil
	}
	if len(files) == 0 {
		return c.notImported(it, "the WXR tells no file of it"), nil
	}
	var failed []notUsed
	var won *candidate
	for _, f := range files {
		cand, err := c.try(ctx, it, id, uploadedAt, f)
		if err != nil {
			return mediaResult{}, err
		}
		if cand.why != "" {
			failed = append(failed, notUsed{name: f.name, why: cand.why})
			continue
		}
		won = &cand
		break
	}
	if won == nil {
		reasons := make([]string, len(failed))
		for i, f := range failed {
			reasons[i] = fmt.Sprintf("%q: %s", f.name, f.why)
		}
		return c.notImported(it, strings.Join(reasons, "; ")), nil
	}
	defer won.orig.discard()

	entries := won.entries
	// Only the original image can be tried before another file, so it is the one that was not used.
	if len(failed) > 0 {
		entries = append(entries, c.entry(it, id, Review, "",
			"the original image %q is not imported (%s); the file %q that WordPress made of it is imported",
			failed[0].name, failed[0].why, won.file.name))
	}
	if editedFrom != "" {
		entries = append(entries, c.entry(it, id, Review, "",
			"the image was edited in WordPress: the edited file %q is imported, not the original image %q",
			won.file.name, editedFrom))
	}
	if won.media.Kind() == content.MediaImage {
		data, err := won.orig.read()
		if err != nil {
			return mediaResult{}, fmt.Errorf("saving an original in %s: %w", won.orig.dir, err)
		}
		if _, err := imaging.Inspect(data); err != nil {
			entries = append(entries, c.entry(it, id, Review, "",
				"the build cannot make the published files of the image: %v; it fails if something published uses the image", err))
		}
	}
	if err := won.orig.keep(); err != nil {
		return mediaResult{}, err
	}
	return mediaResult{media: won.media, entries: entries}, nil
}

// notImported is the result for an attachment that is not imported, for the reason why.
func (c *mediaConv) notImported(it *wxr.Item, why string) mediaResult {
	e := Entry{Kind: Skipped, Source: ItemSource(c.imp.File, it), Message: "the attachment is not imported: " + why}
	return mediaResult{entries: []Entry{e}}
}

// entry makes an entry about the attachment it at pointer in its medium, which has the ID id.
func (c *mediaConv) entry(it *wxr.Item, id string, kind Kind, pointer, format string, args ...any) Entry {
	return Entry{
		Kind:    kind,
		Source:  ItemSource(c.imp.File, it),
		Message: fmt.Sprintf(format, args...),
		Target:  documentTarget(content.RefMedia, id, pointer),
	}
}

// try reads f and makes a medium of it. If it cannot, the candidate has the reason, and no file is left.
func (c *mediaConv) try(ctx context.Context, it *wxr.Item, id, uploadedAt string, f mediaFile) (candidate, error) {
	orig, err := c.readFile(ctx, f)
	if err != nil {
		var skip *skipError
		if errors.As(err, &skip) {
			return candidate{why: skip.reason}, nil
		}
		return candidate{}, err
	}
	m, entries := c.document(it, id, uploadedAt, f, orig)
	if err := m.Validate(c.site); err != nil {
		orig.discard()
		return candidate{why: errorText(err)}, nil
	}
	return candidate{media: &m, orig: orig, file: f, entries: entries}, nil
}

// readFile saves f from the copy of the uploads, or if it is not there, from the old site. The error is a *skipError if f
// cannot be read from either, and otherwise one that stops the import.
func (c *mediaConv) readFile(ctx context.Context, f mediaFile) (*original, error) {
	var reasons []string
	if c.opts.Uploads != nil {
		if f.rel == "" {
			reasons = append(reasons, "the WXR does not tell where it is in the uploads directory")
		} else {
			orig, err := c.readCopy(ctx, f.rel)
			if !errors.Is(err, errNotInCopy) {
				return orig, err
			}
			reasons = append(reasons, errNotInCopy.reason)
		}
	}
	if c.opts.Download != nil {
		if f.url == nil {
			reasons = append(reasons, "the WXR gives no address to download it from")
		} else {
			return c.opts.Download.fetch(ctx, c.home, f.url, func(r io.Reader) (*original, error) { return c.files.save(ctx, r) })
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "there is neither a copy of the uploads nor a download to read it from")
	}
	return nil, &skipError{reason: strings.Join(reasons, "; ")}
}

// readCopy saves the file rel of the copy of the uploads.
func (c *mediaConv) readCopy(ctx context.Context, rel string) (*original, error) {
	f, err := openUpload(c.opts.Uploads, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	orig, err := c.files.save(ctx, f)
	var re *readError
	if errors.As(err, &re) {
		return nil, fmt.Errorf("reading the copy of the uploads: %w", re.err)
	}
	return orig, err
}

// document makes the medium of the attachment it from orig, the file f of it, and the entries about what it left out or
// changed. The medium may not be valid.
func (c *mediaConv) document(it *wxr.Item, id, uploadedAt string, f mediaFile, orig *original) (content.Media, []Entry) {
	var entries []Entry
	note := func(kind Kind, pointer, format string, args ...any) {
		entries = append(entries, c.entry(it, id, kind, pointer, format, args...))
	}
	language := c.site.DefaultLanguage
	m := content.Media{
		ID:         id,
		Hash:       orig.hash,
		MIMEType:   orig.info.MIMEType,
		Size:       int(orig.size),
		UploadedAt: uploadedAt,
		UploadedBy: c.opts.By,
	}
	image := content.MediaKind(m.MIMEType) == content.MediaImage

	var altReplaced bool
	var captionLost []string
	var captionErr error
	if image {
		m.Width, m.Height = orig.info.Width, orig.info.Height
		alt, _ := it.Meta.Get("_wp_attachment_image_alt")
		alt, altReplaced = oneLine(alt)
		if alt != "" {
			m.Alt = content.Localized{language: alt}
		}
		if strings.TrimSpace(it.Excerpt) != "" {
			var caption string
			caption, captionLost, captionErr = htmlText(it.Excerpt)
			if captionErr == nil && caption != "" {
				m.Caption = content.Localized{language: caption}
			}
		}
	}
	name, why := fixFileName(f.name)
	m.FileName = name
	// After the others, which may be longer when they are in NFC.
	m.Normalize()

	if image {
		if text := m.Alt[language]; len(text) > content.MaxTextBytes {
			delete(m.Alt, language)
			note(Skipped, "/alt", "the alt is not imported: it is too long: %d bytes (max %d)", len(text), content.MaxTextBytes)
		} else if altReplaced && text != "" {
			note(Changed, "/alt", "line breaks and tabs in the alt are spaces here")
		}
		switch text := m.Caption[language]; {
		case captionErr != nil:
			note(Skipped, "/caption", "the caption is not imported: it is too complex to read as HTML")
		case len(text) > content.MaxTextBytes:
			delete(m.Caption, language)
			note(Skipped, "/caption", "the caption is not imported: it is too long: %d bytes (max %d)", len(text), content.MaxTextBytes)
		case len(captionLost) > 0:
			note(Changed, "/caption", "%s", lostMessage(captionLost))
		}
		if len(m.Alt) == 0 {
			m.Alt = nil
		}
		if len(m.Caption) == 0 {
			m.Caption = nil
		}
	} else if strings.TrimSpace(it.Excerpt) != "" {
		note(Skipped, "", "the caption is not imported; a PDF has no caption in whiteroom")
	}
	if strings.TrimSpace(it.Content) != "" {
		note(Skipped, "", "the description is not imported; whiteroom media have no descriptions")
	}
	if len(why) > 0 {
		note(Changed, "/fileName", "the file name %q is %q here: %s", f.name, name, strings.Join(why, "; "))
	}
	return m, entries
}
