//go:build linux

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The data sets of the benchmarks (bench_test.go, 3.5). What they hold depends on benchSeed alone. Each save is made by
// a generator of its own, so that a process can make any save again, in the same way for every backend and run.

const benchSeed = 63

// benchSet is a data set: entries, terms and media that are each saved once, after which second of the entries, chosen
// at random, are saved again; or, if rounds is not 0, entries alone that are each saved rounds times, the first of
// which makes them. Every save of an entry adds a revision, and every save appends events audit events. An Update of
// the load has batch saves.
type benchSet struct {
	name                          string
	entries, terms, media         int
	second, rounds, events, batch int
}

var benchSets = []benchSet{
	{name: "docs-1k", entries: 700, terms: 200, media: 100, second: 140, events: 1, batch: 500},
	{name: "docs-10k", entries: 7_000, terms: 2_000, media: 1_000, second: 1_400, events: 1, batch: 500},
	{name: "docs-50k", entries: 35_000, terms: 10_000, media: 5_000, second: 7_000, events: 1, batch: 500},
	{name: "history-heavy", entries: 10_000, rounds: 10, events: 10, batch: 100},
}

// benchSmoke is the set of WHITEROOM_BENCH_SMOKE.
var benchSmoke = benchSet{name: "smoke", entries: 70, terms: 20, media: 10, second: 14, events: 1, batch: 500}

func (bs benchSet) documents() int {
	return bs.entries + bs.terms + bs.media
}

// saves is the number of saves of the load, which is that of the versions.
func (bs benchSet) saves() int {
	if bs.rounds > 0 {
		return bs.entries * bs.rounds
	}
	return bs.documents() + bs.second
}

func (bs benchSet) revisions() int {
	if bs.rounds > 0 {
		return bs.entries * bs.rounds
	}
	return bs.entries + bs.second
}

type benchKind uint8

const (
	benchEntry benchKind = iota
	benchTerm
	benchMedia
)

var benchRefKinds = [...]content.RefKind{benchEntry: content.RefEntry, benchTerm: content.RefTerm,
	benchMedia: content.RefMedia}

// benchID returns an ID with prefix, then "01JK" and n in 22 digits.
func benchID(prefix string, n int) string {
	return fmt.Sprintf("%s01JK%022d", prefix, n)
}

// The streams of the generators: each save of a stream, and the order of each stage, has a generator of its own.
const (
	benchStreamSet        uint64 = iota + 1 // the saves of the load
	benchStreamOps                          // the single saves of ops
	benchStreamLoop                         // the single saves of the process that is killed before open-killed
	benchStreamCheckpoint                   // the single save before the checkpoint
	benchStreamOrder                        // the order of the saves of a stage
	benchStreamPicks                        // the documents that ops reads
)

func benchRand(stream uint64, n int) *rand.Rand {
	return rand.New(rand.NewPCG(benchSeed, stream<<40|uint64(n)))
}

// benchRevisionBase is, for each stream of single saves, the number before that of the revision of its first save.
var benchRevisionBase = map[uint64]int{
	benchStreamOps: 100_000_000, benchStreamLoop: 200_000_000, benchStreamCheckpoint: 300_000_000,
}

// benchRef names a save of the document index (from 1) of kind. save is its number in its stream (from 1), and rev
// that of the revision that it adds if the document is an entry.
type benchRef struct {
	kind             benchKind
	index, save, rev int
}

// stages returns the names of the stages of the load of bs, in order.
func (bs benchSet) stages() []string {
	if bs.rounds == 0 {
		return []string{"documents", "second saves"}
	}
	names := make([]string, bs.rounds)
	for i := range names {
		names[i] = "round " + strconv.Itoa(i+1)
	}
	return names
}

// stage returns the saves of the stage i of the load of bs, in order: the terms, the media and the entries in the order
// of their IDs, then the second saves in a random order; or, in each round, every entry once in a random order.
func (bs benchSet) stage(i int) []benchRef {
	if bs.rounds > 0 {
		order := benchRand(benchStreamOrder, i).Perm(bs.entries)
		refs := make([]benchRef, len(order))
		for j, e := range order {
			n := i*bs.entries + j + 1
			refs[j] = benchRef{kind: benchEntry, index: e + 1, save: n, rev: n}
		}
		return refs
	}
	if i == 1 {
		order := benchRand(benchStreamOrder, i).Perm(bs.entries)[:bs.second]
		refs := make([]benchRef, len(order))
		for j, e := range order {
			refs[j] = benchRef{kind: benchEntry, index: e + 1, save: bs.documents() + j + 1, rev: bs.entries + j + 1}
		}
		return refs
	}
	refs := make([]benchRef, 0, bs.documents())
	for _, part := range []struct {
		kind benchKind
		n    int
	}{{benchTerm, bs.terms}, {benchMedia, bs.media}, {benchEntry, bs.entries}} {
		for index := 1; index <= part.n; index++ {
			ref := benchRef{kind: part.kind, index: index, save: len(refs) + 1}
			if part.kind == benchEntry {
				ref.rev = index
			}
			refs = append(refs, ref)
		}
	}
	return refs
}

// benchSave is what a save puts in an Update: a version of a document, the revision of an entry, and audit events.
type benchSave struct {
	kind   content.RefKind
	id     string
	doc    []byte
	revID  string // "" for a term or a medium
	rev    []byte
	by     string
	events [][]byte
}

const benchUsers = 10

func benchUser(n int) string {
	return benchID("usr_", n)
}

// save makes the save ref of bs with r, its generator.
func (bs benchSet) save(r *rand.Rand, ref benchRef) benchSave {
	sv := benchSave{kind: benchRefKinds[ref.kind], by: benchUser(1 + r.IntN(benchUsers))}
	switch ref.kind {
	case benchEntry:
		sv.id = benchID("ent_", ref.index)
		sv.revID = benchID("rev_", ref.rev)
		sv.doc = benchEntryJSON(r, ref.index, sv.revID)
		sv.rev = benchRevisionJSON(r, ref.rev)
	case benchTerm:
		sv.id = benchID("trm_", ref.index)
		sv.doc = benchTermJSON(r, ref.index)
	case benchMedia:
		sv.id = benchID("med_", ref.index)
		sv.doc = benchMediaJSON(r, ref.index)
	}
	for k := range bs.events {
		sv.events = append(sv.events, benchEventJSON(r, sv.id, sv.by, ref.save*bs.events+k))
	}
	return sv
}

func (bs benchSet) loadSave(ref benchRef) benchSave {
	return bs.save(benchRand(benchStreamSet, ref.save), ref)
}

// single makes the save n of a stream of single saves: a new version of an entry chosen at random, with a new revision
// and one audit event.
func (bs benchSet) single(stream uint64, n int) benchSave {
	r := benchRand(stream, n)
	ref := benchRef{kind: benchEntry, index: 1 + r.IntN(bs.entries), save: n, rev: benchRevisionBase[stream] + n}
	bs.events = 1
	return bs.save(r, ref)
}

// The sizes of the JSON of the documents and the events, which take a size between min and max at random; the
// revisions take benchRevisionSize.
var benchSizes = map[string][2]int{
	"entry": {320, 800}, "term": {250, 400}, "media": {330, 400}, "event": {130, 170},
}

func benchSize(r *rand.Rand, what string) int {
	s := benchSizes[what]
	return s[0] + r.IntN(s[1]-s[0]+1)
}

// The sizes of the JSON of the revisions: 1% of them large, between benchLarge[0] and benchLarge[1]; the others
// log-normal with a median of 2 KiB and 90% at or below 8 KiB, held between benchSmall[0] and benchSmall[1].
var (
	benchLarge = [2]int{100 << 10, 400 << 10}
	benchSmall = [2]int{256, 64<<10 - 1}
)

const (
	benchMedian = 2048
	benchZ90    = 1.2815515655446004 // the 90th percentile of the standard normal distribution
)

func benchRevisionSize(r *rand.Rand) int {
	if r.IntN(100) == 0 {
		return benchLarge[0] + r.IntN(benchLarge[1]-benchLarge[0]+1)
	}
	n := int(math.Round(benchMedian * math.Exp(r.NormFloat64()*math.Log(4)/benchZ90)))
	return min(max(n, benchSmall[0]), benchSmall[1])
}

// benchCreated returns when the document index was made: 37 seconds apart from 2026-01-01 in JST.
func benchCreated(index int) string {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	return t.Add(time.Duration(index) * 37 * time.Second).Format(time.RFC3339)
}

// benchPad ends b, a JSON object without its closing brace, with spaces and the brace, so that it has size bytes, or
// with the brace alone if it is not shorter.
func benchPad(b []byte, size int) []byte {
	for len(b) < size-1 {
		b = append(b, ' ')
	}
	return append(b, '}')
}

// benchEntryJSON returns the JSON of the entry index, which publishes the revision rev: the keys of an entry of the
// sample site.
func benchEntryJSON(r *rand.Rand, index int, rev string) []byte {
	typ := "post"
	if index%5 == 0 {
		typ = "page"
	}
	b := make([]byte, 0, benchSizes["entry"][1])
	b = append(b, `{"schemaVersion":1,"id":"`+benchID("ent_", index)+`","number":`...)
	b = strconv.AppendInt(b, int64(index), 10)
	b = append(b, `,"type":"`+typ+`","language":"ja","translationGroup":"`+benchID("tg_", index)+
		`","status":"published","publishedRevision":"`+rev+`","createdAt":"`+benchCreated(index)+
		`","createdBy":"`+benchUser(1+index%benchUsers)+`"`...)
	return benchPad(b, benchSize(r, "entry"))
}

func benchTermJSON(r *rand.Rand, index int) []byte {
	taxonomy := "category"
	if index%2 == 0 {
		taxonomy = "tag"
	}
	b := make([]byte, 0, benchSizes["term"][1])
	b = append(b, `{"schemaVersion":1,"id":"`+benchID("trm_", index)+`","taxonomy":"`+taxonomy+
		`","language":"ja","translationGroup":"`+benchID("tg_", 1_000_000+index)+`","name":"`...)
	b = benchText(r, b, 6+3*r.IntN(7))
	b = append(b, `","slug":"term-`...)
	b = strconv.AppendInt(b, int64(index), 10)
	b = append(b, '"')
	return benchPad(b, benchSize(r, "term"))
}

// benchMediaJSON returns the JSON of the medium index, an image whose original the store does not have.
func benchMediaJSON(r *rand.Rand, index int) []byte {
	sum := sha256.Sum256([]byte("whiteroom bench media " + strconv.Itoa(index)))
	b := make([]byte, 0, benchSizes["media"][1])
	b = append(b, `{"schemaVersion":1,"id":"`+benchID("med_", index)+`","hash":"sha256:`+hex.EncodeToString(sum[:])+
		`","fileName":"photo-`+strconv.Itoa(index)+`.jpg","mimeType":"image/jpeg","size":`...)
	b = strconv.AppendInt(b, int64(20_000+r.IntN(2_000_000)), 10)
	b = append(b, `,"width":`...)
	b = strconv.AppendInt(b, int64(640+r.IntN(3_361)), 10)
	b = append(b, `,"height":`...)
	b = strconv.AppendInt(b, int64(480+r.IntN(2_521)), 10)
	b = append(b, `,"uploadedAt":"`+benchCreated(index)+`","uploadedBy":"`+benchUser(1+index%benchUsers)+`"`...)
	return benchPad(b, benchSize(r, "media"))
}

// The parts of a paragraph of the revisions around its ID and its text.
const (
	benchParagraph = `{"type":"paragraph","id":"`
	benchContent   = `","content":[{"type":"text","text":"`
	benchEnd       = `"}]}`
	benchBlockID   = len("b_00000000")
)

// benchRevisionJSON returns the JSON of the revision rev, of the size of benchRevisionSize: a title, a slug, and
// paragraphs of 200 to 1,000 bytes of text each, the last of which has what is left.
func benchRevisionJSON(r *rand.Rand, rev int) []byte {
	size := benchRevisionSize(r)
	b := make([]byte, 0, size)
	b = append(b, `{"schemaVersion":1,"title":"`...)
	b = benchText(r, b, 12+r.IntN(25))
	b = append(b, `","slug":"post-`...)
	b = strconv.AppendInt(b, int64(rev), 10)
	b = append(b, `","body":[`...)
	const block = len(benchParagraph) + benchBlockID + len(benchContent) + len(benchEnd) // without the text
	for i := 0; ; i++ {
		sep := min(i, 1)
		left := size - len(b) - sep - block - len("]}") // the text of the paragraph if it is the last
		n := 200 + r.IntN(801)
		if left-n < 1+block+1 { // the next paragraph would not have a byte of text
			n = left
		}
		if sep == 1 {
			b = append(b, ',')
		}
		b = append(b, benchParagraph...)
		b = benchAppendBlockID(b, rev, i)
		b = append(b, benchContent...)
		b = benchText(r, b, n)
		b = append(b, benchEnd...)
		if n == left {
			break
		}
	}
	return append(b, "]}"...)
}

// benchAppendBlockID appends the ID of the paragraph i of the revision rev: "b_" and 8 lower case letters or digits,
// unique among the revisions of the benchmarks.
func benchAppendBlockID(b []byte, rev, i int) []byte {
	s := strconv.FormatInt(int64(rev)<<12|int64(i), 36)
	b = append(b, "b_"...)
	for range benchBlockID - len("b_") - len(s) {
		b = append(b, '0')
	}
	return append(b, s...)
}

// benchText appends n bytes of text to b: hiragana and kanji, 3 bytes each in UTF-8, and spaces, then up to 2 lower
// case letters.
func benchText(r *rand.Rand, b []byte, n int) []byte {
	for n >= 3 {
		switch x := r.IntN(16); {
		case x == 0:
			b = append(b, ' ')
			n--
			continue
		case x < 10:
			b = utf8.AppendRune(b, rune(0x3042+r.IntN(0x3093-0x3042+1))) // hiragana, U+3042 to U+3093
		default:
			b = utf8.AppendRune(b, rune(0x4E00+r.IntN(0xA000-0x4E00))) // CJK unified ideographs
		}
		n -= 3
	}
	for ; n > 0; n-- {
		b = append(b, byte('a'+r.IntN(26)))
	}
	return b
}

// benchEventJSON returns the audit event n of a save of the document id by the user by.
func benchEventJSON(r *rand.Rand, id, by string, n int) []byte {
	b := make([]byte, 0, benchSizes["event"][1])
	b = append(b, `{"type":"save","document":"`+id+`","user":"`+by+`","n":`...)
	b = strconv.AppendInt(b, int64(n), 10)
	b = append(b, `,"note":"`...)
	size := benchSize(r, "event")
	for len(b) < size-len(`"}`) {
		b = append(b, byte('a'+r.IntN(26)))
	}
	return append(b, `"}`...)
}
