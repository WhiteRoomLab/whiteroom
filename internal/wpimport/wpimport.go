// Package wpimport imports a WordPress site from its WXR (section 6.10 of the design document). An import goes in steps,
// and the order is needed: Read reads the WXR; ProposeSettings guesses what the WXR does not hold, such as the time zone
// and the URL rules, as Settings, which the person who imports checks and corrects; the plan decides what to import,
// gives each object of WordPress its ID in a Table, and records the entries and the media with AddRef; Site makes the
// site settings from the corrected Settings, finding the front page, the logo and the like among those records; Terms and
// Authors make the terms and the author profiles in that site; the conversion of the media and the entries then turns each
// into an entity, calling Body for the content of each entry to get its blocks, finds the IDs of the others with the
// Table's Lookup and Ref, and drops from the Table what it cannot import; the writing puts the entities into the site
// directory. Only reading, the settings, the IDs, the report of what was not imported, Import (the state of an import),
// the conversion of the terms, of the authors and of the site settings, the conversion of the content of the block editor
// and of the classic editor into blocks (Body), and the guess of the permalinks are here so far.
package wpimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/phpserialize"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// Read reads the WXR in r, which is called name in the report and in errors. A problem in the file is a *content.Error
// at name:line:column. What Read changed in the values, and the comments, which are not imported, are in the report.
func Read(ctx context.Context, name string, r io.Reader) (*wxr.Export, *Report, error) {
	e, err := wxr.Read(ctx, r, wxr.Options{})
	if err != nil {
		var we *wxr.Error
		if errors.As(err, &we) {
			return nil, nil, &content.Error{File: name, Line: we.Line, Column: we.Column, Msg: we.Msg}
		}
		return nil, nil, err
	}
	rep := NewReport(Info{File: name, Version: e.Version, WordPress: e.WordPress, Site: e.Channel.Link})
	for _, n := range e.Notes {
		src := noteSource(name, e, n)
		src.Line, src.Column = n.Line, n.Column
		rep.Add(Entry{Kind: Changed, Source: src, Message: n.Msg})
	}
	comments, items := 0, 0
	for i := range e.Items {
		if c := e.Items[i].Comments; c > 0 {
			comments += c
			items++
		}
	}
	if comments > 0 {
		verb := "are"
		if comments == 1 {
			verb = "is"
		}
		msg := fmt.Sprintf("%s on %s %s not imported; whiteroom has no comments",
			count(comments, "comment"), count(items, "item"), verb)
		rep.Add(Entry{Kind: Skipped, Source: ChannelSource(name, e), Message: msg})
	}
	return e, rep, nil
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// noteSource returns the source of the record that the note is about.
func noteSource(file string, e *wxr.Export, n wxr.Note) Source {
	switch n.Record {
	case wxr.RecordChannel:
		return ChannelSource(file, e)
	case wxr.RecordAuthor:
		return AuthorSource(file, &e.Authors[n.Index])
	case wxr.RecordTerm:
		return TermSource(file, &e.Terms[n.Index])
	case wxr.RecordItem:
		return ItemSource(file, &e.Items[n.Index])
	}
	return Source{File: file}
}

func ChannelSource(file string, e *wxr.Export) Source {
	return Source{File: file, Line: e.Channel.Line, Column: e.Channel.Column, Record: recordChannel}
}

func AuthorSource(file string, a *wxr.Author) Source {
	return authorSource(file, a.Pos, a.ID, a.Login, a.Email)
}

// authorSource returns the source of the author with the login, at pos. A login that is an email address, or is the email
// address of the author, is not named: the ID tells the author, or, if there is none, the slug that the login makes.
func authorSource(file string, pos wxr.Pos, id int64, login, email string) Source {
	s := Source{File: file, Line: pos.Line, Column: pos.Column, Record: recordAuthor}
	switch {
	case !hidesLogin(login, email):
		s.ID, s.Name = id, login
	case id >= 1:
		s.ID = id
	default:
		s.Name = nicename(login)
	}
	return s
}

func TermSource(file string, t *wxr.Term) Source {
	return Source{File: file, Line: t.Line, Column: t.Column, Record: recordTerm, ID: t.ID, Type: t.Taxonomy, Name: t.Name}
}

func ItemSource(file string, it *wxr.Item) Source {
	return Source{File: file, Line: it.Line, Column: it.Column, Record: recordItem, ID: it.ID, Type: it.Type, Name: it.Title}
}

// MetaSource returns the source of the postmeta or termmeta m of the record s, at the place of m.
func MetaSource(s Source, m wxr.Meta) Source {
	s.Meta = m.Key
	s.Line, s.Column = m.Line, m.Column
	return s
}

// phpSpace is the set of characters that PHP's trim removes by default.
const phpSpace = " \t\n\r\v\x00"

// Unserialize returns the value of m, a postmeta or termmeta of the record src, as WordPress's maybe_unserialize does:
// if it looks serialized, the value that it serializes, and if not, the string as written. A value that cannot be read
// is a *content.Error at the place of m, with the byte of the value where reading failed. If it can only be read by
// counting the LF in a string as CRLF, which an XML parser turned into LF, it is read so, and rep, if it is not nil, has
// an entry for it.
func Unserialize(rep *Report, src Source, m wxr.Meta) (phpserialize.Value, error) {
	if !phpserialize.LooksSerialized(m.Value) {
		return phpserialize.Value{Kind: phpserialize.String, Str: m.Value}, nil
	}
	s := strings.TrimLeft(m.Value, phpSpace)
	lead := len(m.Value) - len(s)
	s = strings.TrimRight(s, phpSpace)
	v, err := phpserialize.Unserialize(s)
	if err == nil {
		return v, nil
	}
	if v, n, crlfErr := phpserialize.UnserializeCRLF(s); crlfErr == nil {
		if rep != nil {
			msg := fmt.Sprintf("read the serialized value by counting the LF line breaks in %s as CRLF "+
				"(XML turns CRLF into LF); the line breaks are kept as LF", count(n, "string"))
			rep.Add(Entry{Kind: Changed, Source: MetaSource(src, m), Message: msg})
		}
		return v, nil
	}
	msg := err.Error()
	var pe *phpserialize.Error
	if errors.As(err, &pe) {
		msg = fmt.Sprintf("byte %d: %s", lead+pe.Offset, pe.Msg)
	}
	return phpserialize.Value{}, &content.Error{
		File: src.File, Line: m.Line, Column: m.Column,
		Msg: fmt.Sprintf("the value of %q: %s", m.Key, msg),
	}
}
