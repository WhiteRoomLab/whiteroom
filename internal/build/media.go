package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/imaging"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
)

// resolver is the render.Resolver of the build, which also gives the rest of the build the files of media. It inspects
// the original of an image when it is first asked about it, reading a few originals at once at most, and records the
// media that it is asked about, with the files of the images: those are the media that the build publishes
// (publishMedia). It is safe for concurrent use.
type resolver struct {
	b      *builder
	images *imaging.Images
	sem    chan struct{} // holds a value for each original that is inspected or read at once

	mu    sync.Mutex
	infos map[string]func() (imaging.Info, error) // by hash, each called once
	asked map[string]map[imaging.Variant]bool     // the media asked about by ID, with the files of the images
}

// newResolver returns the resolver of b, which makes the files of images with images and reads inspecting originals at
// once at most.
func newResolver(b *builder, images *imaging.Images, inspecting int) *resolver {
	return &resolver{
		b:      b,
		images: images,
		sem:    make(chan struct{}, max(inspecting, 1)),
		infos:  make(map[string]func() (imaging.Info, error)),
		asked:  make(map[string]map[imaging.Variant]bool),
	}
}

// mediaFile is a file of a medium.
type mediaFile struct {
	path          string // from the site URL, not encoded, such as "/media/<ID>/photo-640x427.jpg"
	version       string
	width, height int // of the image in it; 0 for a PDF
}

// versioned returns the path of f with "?v=" and its version, which pages refer to it by.
func (f mediaFile) versioned() string {
	return f.path + "?v=" + f.version
}

// icon is a square PNG of a site icon (6.6.2).
type icon struct {
	size          int    // the side that it is for, one of imaging.IconSizes
	path          string // from the site URL, not encoded, with "?v=" and its version
	width, height int    // of the file, which are less than size if the image is smaller
}

// Entry returns the title of the published revision of the entry with the ID and the path of its page; both are "" if
// it is not published.
func (r *resolver) Entry(id string) (title, path string, ok bool) {
	d := r.b.entries[id]
	if d == nil {
		return "", "", false
	}
	if d.rev != nil {
		title = d.rev.Title
	}
	return title, r.b.entryPaths[id], true
}

// Media returns the medium with the ID and the path of its published file with its version, and records it as asked
// about. ok is false if there is no such medium or the original of the image cannot be inspected, which publishMedia
// reports.
func (r *resolver) Media(id string) (m *content.Media, path string, ok bool) {
	d := r.b.media[id]
	if d == nil {
		return nil, "", false
	}
	f, err := r.published(id, d)
	if err != nil {
		return nil, "", false
	}
	return &d.v, f.versioned(), true
}

// Image returns the files that show the image medium with the ID at size, or at the default size if size is nil, as
// imaging.ForSize chooses them, with their versions, and records them and its published file as asked about. ok is
// false if there is no such image or its original cannot be inspected, which publishMedia reports.
func (r *resolver) Image(id string, size *content.ImageSize) (files render.ImageFiles, ok bool) {
	d := r.b.media[id]
	if d == nil || d.v.Kind() != content.MediaImage {
		return render.ImageFiles{}, false
	}
	info, err := r.image(id, d)
	if err != nil {
		return render.ImageFiles{}, false
	}
	set := imaging.ForSize(info, size)
	r.record(id, set.Src)
	r.record(id, set.Srcset...)
	files = render.ImageFiles{Src: imageFile(id, &d.v, info, set.Src).versioned(), Width: set.Width, Height: set.Height}
	for _, v := range set.Srcset {
		f := imageFile(id, &d.v, info, v)
		files.Srcset = append(files.Srcset, render.ImageFile{Path: f.versioned(), Width: f.width})
	}
	return files, true
}

func (r *resolver) Form(id string) (f *content.Form, ok bool) {
	if d := r.b.forms[id]; d != nil {
		return &d.v, true
	}
	return nil, false
}

// file returns the path of the published file of the medium with the ID, without its version, and the size of the
// image in it (0 for a PDF), and records it as asked about. The error is that of inspecting the original of an image.
func (r *resolver) file(id string) (path string, width, height int, err error) {
	d := r.b.media[id]
	if d == nil {
		return "", 0, 0, fmt.Errorf("media %q does not exist", id)
	}
	f, err := r.published(id, d)
	return f.path, f.width, f.height, err
}

// mediaPath returns the path of the published file of the medium with the ID, without its version, as file does, for
// the redirects to it (resolveRedirects).
func (r *resolver) mediaPath(id string) (string, error) {
	path, _, _, err := r.file(id)
	return path, err
}

// icons returns the icons of the image medium with the ID for each of imaging.IconSizes, in its order, and records them
// and its published file as asked about; icons of the same size are the same file, which can be the published file.
// It returns nil if there is no such image or its original cannot be inspected, which publishMedia reports.
func (r *resolver) icons(id string) []icon {
	d := r.b.media[id]
	if d == nil || d.v.Kind() != content.MediaImage {
		return nil
	}
	info, err := r.image(id, d)
	if err != nil {
		return nil
	}
	var icons []icon
	for _, n := range imaging.IconSizes() {
		v := imaging.Icon(info, n)
		r.record(id, v)
		f := imageFile(id, &d.v, info, v)
		icons = append(icons, icon{size: n, path: f.versioned(), width: f.width, height: f.height})
	}
	return icons
}

// published returns the published file of d, the medium with the ID id, and records it as asked about.
func (r *resolver) published(id string, d *doc[content.Media]) (mediaFile, error) {
	if d.v.Kind() == content.MediaPDF {
		r.record(id)
		path := imaging.Path(id, imaging.PDFName(d.v.FileName))
		return mediaFile{path: path, version: imaging.PDFVersion(d.v.Hash)}, nil
	}
	info, err := r.image(id, d)
	if err != nil {
		return mediaFile{}, err
	}
	return imageFile(id, &d.v, info, imaging.Published(info)), nil
}

// image returns what imaging reads from the original of d, the image medium with the ID id, and records d with its
// published file as asked about, or only d if the original cannot be inspected.
func (r *resolver) image(id string, d *doc[content.Media]) (imaging.Info, error) {
	info, err := r.info(d.v.Hash)
	if err != nil {
		r.record(id)
		return imaging.Info{}, fmt.Errorf("cannot read the original: %w", err)
	}
	r.record(id, imaging.Published(info))
	return info, nil
}

// info returns what imaging.Images.Info reads from the original whose Media.Hash is hash, calling it once for each
// hash, and cap(r.sem) times at once at most.
func (r *resolver) info(hash string) (imaging.Info, error) {
	r.mu.Lock()
	info := r.infos[hash]
	if info == nil {
		info = sync.OnceValues(func() (imaging.Info, error) {
			r.sem <- struct{}{}
			defer func() { <-r.sem }()
			return r.images.Info(hash, r.open(hash))
		})
		r.infos[hash] = info
	}
	r.mu.Unlock()
	return info()
}

// open returns the function that opens the original whose Media.Hash is hash, for imaging.
func (r *resolver) open(hash string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return r.b.src.OpenMedia(hash) }
}

// record records the medium with the ID as asked about, with vs, files of an image.
func (r *resolver) record(id string, vs ...imaging.Variant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.asked[id] == nil {
		r.asked[id] = make(map[imaging.Variant]bool)
	}
	for _, v := range vs {
		r.asked[id][v] = true
	}
}

// imageFile returns the file v of m, the image medium with the ID id, whose original imaging read as info.
func imageFile(id string, m *content.Media, info imaging.Info, v imaging.Variant) mediaFile {
	return mediaFile{
		path:    imaging.Path(id, imaging.Name(m.FileName, info, v)),
		version: imaging.Version(m.Hash, v),
		width:   v.Width,
		height:  v.Height,
	}
}

// publishMedia publishes the media that b.res was asked about, in at most workers goroutines: it checks the original of
// each against its document, and writes into pub, at the paths of imaging.Path without the leading "/", the files of
// each image that b.res was asked about with its published file, and each PDF as it is. It adds them to b.outputs, in
// the order of the IDs. It opens no other originals. The problems of the media go to p and the warnings about their
// originals to b.warnings, at their documents. The error is one of b.src or of writing, or ctx.Err().
func (b *builder) publishMedia(ctx context.Context, pub *os.Root, workers int, p *problems) error {
	b.res.mu.Lock()
	ids := slices.Sorted(maps.Keys(b.res.asked))
	vs := make([][]imaging.Variant, len(ids))
	for i, id := range ids {
		vs[i] = slices.Collect(maps.Keys(b.res.asked[id]))
	}
	b.res.mu.Unlock()
	outs := make([][]output, len(ids))
	err := forEach(ctx, len(ids), workers, func(i int) error {
		var err error
		outs[i], err = b.publishMedium(ctx, pub, ids[i], vs[i], p)
		return err
	})
	if err != nil {
		return err
	}
	for _, o := range outs {
		b.outputs = append(b.outputs, o...)
	}
	return nil
}

// publishMedium checks the original of the medium with the ID against its document, adding the problems to p, and
// writes its files into pub: the files vs of an image, or a PDF. It returns their outputs, or none if the medium has
// problems. It opens the original once, to check that it exists and has the size of the document, and to read a PDF;
// imaging reads the original of an image again only to make the files that its cache does not have.
func (b *builder) publishMedium(
	ctx context.Context, pub *os.Root, id string, vs []imaging.Variant, p *problems,
) ([]output, error) {
	d := b.media[id]
	f, err := b.src.OpenMedia(d.v.Hash)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p.add(d.file, "/hash", "no original with this hash")
		return nil, nil
	case err != nil:
		return nil, err
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	switch {
	case err != nil:
		return nil, err
	case stat.Size() != int64(d.v.Size):
		p.add(d.file, "/size", "the original has %d bytes, not %d", stat.Size(), d.v.Size)
		return nil, nil
	case d.v.Kind() == content.MediaPDF:
		return b.publishPDF(ctx, pub, id, d, f, p)
	}
	return b.publishImage(ctx, pub, id, d, vs, p)
}

// publishImage checks what imaging read from the original of d, the image medium with the ID id, against d, and writes
// its files vs, which imaging makes, into pub. It returns their outputs, or none if d has problems, which go to p; the
// warnings about the original go to b.warnings.
func (b *builder) publishImage(
	ctx context.Context, pub *os.Root, id string, d *doc[content.Media], vs []imaging.Variant, p *problems,
) ([]output, error) {
	m := &d.v
	info, err := b.res.info(m.Hash)
	if err != nil {
		p.add(d.file, "", "cannot read the original: %v", err)
		return nil, nil
	}
	ok := true
	if info.MIMEType != m.MIMEType {
		p.add(d.file, "/mimeType", "the original is %s, not %s", info.MIMEType, m.MIMEType)
		ok = false
	}
	if info.Width != m.Width || info.Height != m.Height {
		pointer := "/width"
		if info.Width == m.Width {
			pointer = "/height"
		}
		p.add(d.file, pointer, "the original is %dx%d pixels as displayed, not %dx%d",
			info.Width, info.Height, m.Width, m.Height)
		ok = false
	}
	if !ok {
		return nil, nil
	}
	for _, w := range info.Warnings {
		b.warnings.add(d.file, "", "%s", w)
	}
	// By name, which differs for each file of a medium, so that the outputs come in a fixed order.
	slices.SortFunc(vs, func(x, y imaging.Variant) int {
		return strings.Compare(imaging.Name(m.FileName, info, x), imaging.Name(m.FileName, info, y))
	})
	files, err := b.res.images.Files(ctx, m.Hash, b.res.open(m.Hash), info, vs)
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil:
		p.add(d.file, "", "cannot make the files: %v", err)
		return nil, nil
	}
	outs := make([]output, len(vs))
	for i, v := range vs {
		outs[i] = mediaOutput(id, d, imaging.Name(m.FileName, info, v))
		if err := writeFile(pub, outs[i].file, files[i]); err != nil {
			return nil, err
		}
	}
	return outs, nil
}

// publishPDF checks f, the original of d, the PDF medium with the ID id, against d, and writes it into pub as it is,
// reading cap(b.res.sem) originals at once at most. It returns its output, or none if d has problems, which go to p.
func (b *builder) publishPDF(
	ctx context.Context, pub *os.Root, id string, d *doc[content.Media], f fs.File, p *problems,
) ([]output, error) {
	select {
	case b.res.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-b.res.sem }()
	m := &d.v
	data := make([]byte, m.Size)
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, fmt.Errorf("%s: cannot read the original: %w", d.file.Name, err)
	}
	sum := sha256.Sum256(data)
	if hash := "sha256:" + hex.EncodeToString(sum[:]); hash != m.Hash {
		p.add(d.file, "/hash", "the original has the hash %s", hash)
		return nil, nil
	}
	info, err := mediafile.Probe(bytes.NewReader(data))
	switch {
	case err != nil:
		p.add(d.file, "/mimeType", "the original is not %s: %v", m.MIMEType, err)
		return nil, nil
	case info.MIMEType != m.MIMEType:
		p.add(d.file, "/mimeType", "the original is %s, not %s", info.MIMEType, m.MIMEType)
		return nil, nil
	}
	o := mediaOutput(id, d, imaging.PDFName(m.FileName))
	if err := writeFile(pub, o.file, data); err != nil {
		return nil, err
	}
	return []output{o}, nil
}

// mediaOutput returns the output of the file name of d, the medium with the ID id.
func mediaOutput(id string, d *doc[content.Media], name string) output {
	path := imaging.Path(id, name)
	return output{
		path: path,
		file: strings.TrimPrefix(path, "/"),
		src:  source{file: d.file, pointer: "/fileName", what: d.file.Name},
		rank: rankDocument,
	}
}
