package edit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// replacer is a document that checks what it changes of the version that it replaces, which Save tells it.
type replacer interface{ replaces(cur []byte) }

// mediaDoc is a medium, decoded, as a document that Save handles: Save changes its metadata, not the items that the file
// and the upload give, which ReplaceMedia replaces together.
type mediaDoc struct {
	m   content.Media
	cur *content.Media // the version that Save replaces, if it decodes
}

func (d *mediaDoc) unmarshal(data []byte) error { return d.m.UnmarshalJSON(data) }
func (d *mediaDoc) id() *string                 { return &d.m.ID }
func (d *mediaDoc) value() any                  { return &d.m }
func (d *mediaDoc) normalize()                  { d.m.Normalize() }
func (d *mediaDoc) marshal() ([]byte, error)    { return d.m.MarshalJSON() }

func (d *mediaDoc) replaces(cur []byte) {
	var m content.Media
	if m.UnmarshalJSON(cur) == nil {
		d.cur = &m
	}
}

// validate validates d as Media.Validate does, which looks up nothing, and adds the problems of the items that d changes
// from the version that it replaces, which come from the file and the upload.
func (d *mediaDoc) validate(site *content.Site, _ lookupFunc) error {
	var es content.Errors
	if err := d.m.Validate(site); err != nil && !errors.As(err, &es) {
		return err
	}
	if d.cur != nil {
		es = append(es, d.fromFile()...)
	}
	if len(es) == 0 {
		return nil
	}
	return es
}

func (d *mediaDoc) fromFile() content.Errors {
	m, cur := &d.m, d.cur
	var es content.Errors
	for _, c := range []struct {
		key  string
		same bool
	}{
		{"hash", m.Hash == cur.Hash},
		{"mimeType", m.MIMEType == cur.MIMEType},
		{"size", m.Size == cur.Size},
		{"width", m.Width == cur.Width},
		{"height", m.Height == cur.Height},
		{"uploadedAt", m.UploadedAt == cur.UploadedAt},
		{"uploadedBy", m.UploadedBy == cur.UploadedBy},
	} {
		if !c.same {
			es = append(es, &content.Error{Path: "/" + c.key, Msg: "set from the file; replace the file to change it"})
		}
	}
	return es
}

// failures passes on the reads of r and keeps the first error other than io.EOF.
type failures struct {
	r   io.Reader
	err error
}

func (f *failures) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && f.err == nil {
		f.err = err
	}
	return n, err
}

// probe reads what the original with the hash is from the store, as mediafile.Probe reads a file. If it is not a file of
// a supported format, bad says why, which is a problem of the file; err is an error of the store.
func (e *Editor) probe(hash string) (info mediafile.Info, bad, err error) {
	f, err := e.store.OpenOriginal(hash)
	if err != nil {
		return mediafile.Info{}, nil, err
	}
	defer func() { _ = f.Close() }()
	rd := &failures{r: f}
	info, bad = mediafile.Probe(rd)
	if rd.err != nil {
		return mediafile.Info{}, nil, rd.err
	}
	return info, bad, nil
}

// saveFile saves what file reads as an original, before the document of the medium (3.4), and reads from it what it is,
// from its contents and never from its name or its declared type (6.6.2). A file that is not of a supported format is a
// problem of the document name, which it would be the file of. A file that the build cannot publish is not refused (item
// 105 of 6.12).
func (e *Editor) saveFile(ctx context.Context, name string, file io.Reader) (store.Original, mediafile.Info, error) {
	o, err := e.store.PutOriginal(ctx, file)
	if err != nil {
		return store.Original{}, mediafile.Info{}, err
	}
	info, bad, err := e.probe(o.Hash)
	switch {
	case err != nil:
		return store.Original{}, mediafile.Info{}, err
	case bad != nil:
		return store.Original{}, mediafile.Info{}, content.Errors{{File: name, Msg: "cannot read the file: " + bad.Error()}}
	}
	return o, info, nil
}

// uploadTime returns the time of the operation in the time zone of site, to the second, as RFC 3339.
func (e *Editor) uploadTime(site *content.Site) (string, error) {
	zone, err := time.LoadLocation(site.TimeZone)
	if err != nil {
		return "", err
	}
	return e.now().In(zone).Format(time.RFC3339), nil
}

// declaredWarning returns the warning, located at /mimeType in f, the document of the medium, that the file is of the
// type got, which its contents give, and not of declared, which the upload said: none if declared is "" or the same.
func declaredWarning(f content.SiteFile, got, declared string) content.Errors {
	if declared == "" || strings.EqualFold(got, declared) {
		return nil
	}
	return problemAt(f, "/mimeType", fmt.Sprintf("the file is %s, not %s as uploaded", got, declared))
}

// AddMedia adds a medium made of the file that file reads, with a new ID. The file is saved as an original before the
// document of the medium (3.4), so that a failure after that leaves an original that nothing uses. Its format, and for an
// image its size as it is displayed (the EXIF orientation applied), are read from its contents; fileName, the name that
// it was uploaded with, is normalized and decides neither (6.6.2), and declaredType, the MIME type that the upload said,
// "" if it did not, only gives a warning if it differs. A file in a format that is not supported is refused, and so is one
// that does not validate, such as an image of more than 100 million pixels. The medium has no alt and no caption: Save
// gives them. Its uploadedAt is the time of the operation in the time zone of the site, to the second, and its uploadedBy
// is op.By.
func (e *Editor) AddMedia(ctx context.Context, op Op, file io.Reader, fileName, declaredType string) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	// Before the file is saved, so that every problem names the document.
	id, err := e.newID(content.RefMedia)
	if err != nil {
		return Result{}, err
	}
	name := docName(content.RefMedia, id)
	o, info, err := e.saveFile(ctx, name, file)
	if err != nil {
		return Result{}, err
	}
	q := request{name: "addMedia", params: []string{o.Hash, fileName, declaredType}}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		uploadedAt, err := e.uploadTime(site)
		if err != nil {
			return Result{}, err
		}
		m := content.Media{
			ID: id, Hash: o.Hash, FileName: fileName, MIMEType: info.MIMEType, Size: int(o.Size),
			Width: info.Width, Height: info.Height, UploadedAt: uploadedAt, UploadedBy: r.by,
		}
		m.Normalize()
		data, err := m.MarshalJSON()
		if err != nil {
			return Result{}, err
		}
		f := content.SiteFile{Name: name, Data: data}
		r.given[name] = data
		if err := m.Validate(site); err != nil {
			return Result{}, sorted(content.LocateErrors(err, f))
		}
		v, err := r.tx.Put(content.RefMedia, id, data, r.by)
		if err != nil {
			return Result{}, err
		}
		after, err := r.model(site, settings)
		if err != nil {
			return Result{}, err
		}
		res := Result{Kind: content.RefMedia, ID: id, Version: v, Warnings: declaredWarning(f, info.MIMEType, declaredType)}
		return res, r.finish(&res, after)
	})
}

// ReplaceMedia replaces the file of the medium id, which must be at the version base (as in Save), with the file that file
// reads. The file is read as AddMedia reads it, and the medium takes its hash, size, MIME type and size in pixels, and
// the time of the operation and op.By as its uploadedAt and uploadedBy, and keeps its fileName, alt and caption (so the
// name of its published file keeps its name before the extension, which the format gives). A file of the other kind, an
// image for a PDF or a PDF for an image, is refused. The original that it replaces stays in the store, as the releases
// that use it need it (6.6.2).
func (e *Editor) ReplaceMedia(ctx context.Context, op Op, id string, base int64, file io.Reader, declaredType string) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	if err := content.CheckID(content.RefMedia, id); err != nil {
		return Result{}, err
	}
	name := docName(content.RefMedia, id)
	o, info, err := e.saveFile(ctx, name, file)
	if err != nil {
		return Result{}, err
	}
	q := request{name: "replaceMedia", params: []string{id, strconv.FormatInt(base, 10), o.Hash, declaredType}}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		cur, err := r.current(content.RefMedia, id, base)
		if err != nil {
			return Result{}, err
		}
		was := content.SiteFile{Name: name, Data: cur.Data}
		var m content.Media
		if err := decodeFile(was, m.UnmarshalJSON); err != nil {
			return Result{}, err
		}
		if m.Kind() != content.MediaKind(info.MIMEType) {
			return Result{}, problemAt(was, "/mimeType", fmt.Sprintf(
				"the file is %s; a medium cannot change between an image and a PDF", info.MIMEType))
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		uploadedAt, err := e.uploadTime(site)
		if err != nil {
			return Result{}, err
		}
		m.Hash, m.Size, m.MIMEType, m.Width, m.Height = o.Hash, int(o.Size), info.MIMEType, info.Width, info.Height
		m.UploadedAt, m.UploadedBy = uploadedAt, r.by
		data, err := m.MarshalJSON()
		if err != nil {
			return Result{}, err
		}
		f := content.SiteFile{Name: name, Data: data}
		r.given[name] = data
		if err := m.Validate(site); err != nil {
			return Result{}, sorted(content.LocateErrors(err, f))
		}
		// What lookup tells of the medium now, to tell after the save whether it has changed (Q16-A).
		var old *store.Summary
		tracked := !bytes.Equal(data, cur.Data)
		if tracked {
			if old, err = r.oldSummary(content.RefMedia, id); err != nil {
				return Result{}, err
			}
		}
		v, err := r.tx.Put(content.RefMedia, id, data, r.by)
		if err != nil {
			return Result{}, err
		}
		res := Result{
			Kind: content.RefMedia, ID: id, Version: v, Warnings: declaredWarning(f, info.MIMEType, declaredType),
			Publishes: v.Seq != cur.Seq,
		}
		if v.Seq == cur.Seq {
			return res, nil
		}
		warnings, err := r.checkRefChange(site, content.RefMedia, id, old)
		if err != nil {
			return Result{}, err
		}
		res.Warnings = append(res.Warnings, warnings...)
		after, err := r.model(site, settings)
		if err != nil {
			return Result{}, err
		}
		return res, r.finish(&res, after)
	})
}
