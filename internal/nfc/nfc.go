// Package nfc implements Unicode Normalization Form C (UAX #15) for the Unicode version of Go's unicode package.
package nfc

//go:generate go run ./gen

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"
)

type quickCheck uint8

const (
	qcYes quickCheck = iota // zero, as lookup returns prop{} for runes not in props
	qcMaybe
	qcNo
)

// prop is a rune's ccc, NFC_QC and full canonical decomposition ("" if none).
type prop struct {
	r      rune
	ccc    uint8
	qc     quickCheck
	decomp string
}

type composition struct {
	first, second, composite rune
}

// Hangul constants from section 3.12 of the Unicode Standard.
const (
	sBase  = 0xAC00
	lBase  = 0x1100
	vBase  = 0x1161
	tBase  = 0x11A7
	lCount = 19
	vCount = 21
	tCount = 28
	nCount = vCount * tCount
	sCount = lCount * nCount
)

// char is a rune and its ccc; an invalid UTF-8 byte b is kept as ^rune(b), which is negative.
type char struct {
	r   rune
	ccc uint8
}

// Normalize returns the NFC form of s. Invalid UTF-8 bytes are kept and never combine with other characters.
func Normalize(s string) string {
	if check(s) == qcYes {
		return s
	}
	return normalize(s)
}

func IsNormalized(s string) bool {
	switch check(s) {
	case qcYes:
		return true
	case qcNo:
		return false
	case qcMaybe:
	}
	return normalize(s) == s
}

func check(s string) quickCheck {
	result := qcYes
	var last uint8
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r < utf8.RuneSelf || r == utf8.RuneError && n == 1 {
			last = 0
			continue
		}
		p := lookup(r)
		if p.ccc != 0 && last > p.ccc {
			return qcNo
		}
		switch p.qc {
		case qcYes:
		case qcMaybe:
			result = qcMaybe
		case qcNo:
			return qcNo
		}
		last = p.ccc
	}
	return result
}

func lookup(r rune) prop {
	i, ok := slices.BinarySearchFunc(props[:], r, func(p prop, t rune) int { return cmp.Compare(p.r, t) })
	if !ok {
		return prop{}
	}
	return props[i]
}

func normalize(s string) string {
	cs := decompose(s)
	reorder(cs)
	cs = compose(cs)
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range cs {
		if c.r < 0 {
			b.WriteByte(byte(^c.r))
		} else {
			b.WriteRune(c.r)
		}
	}
	return b.String()
}

func decompose(s string) []char {
	cs := make([]char, 0, len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r < utf8.RuneSelf:
			cs = append(cs, char{r: r})
		case r == utf8.RuneError && n == 1:
			cs = append(cs, char{r: ^rune(s[i])})
		case sBase <= r && r < sBase+sCount:
			si := r - sBase
			cs = append(cs, char{r: lBase + si/nCount}, char{r: vBase + (si%nCount)/tCount})
			if t := si % tCount; t != 0 {
				cs = append(cs, char{r: tBase + t})
			}
		default:
			if p := lookup(r); p.decomp == "" {
				cs = append(cs, char{r, p.ccc})
			} else {
				for _, d := range p.decomp {
					cs = append(cs, char{d, lookup(d).ccc})
				}
			}
		}
		i += n
	}
	return cs
}

func reorder(cs []char) {
	for i := 0; i < len(cs); {
		if cs[i].ccc == 0 {
			i++
			continue
		}
		j := i + 1
		for j < len(cs) && cs[j].ccc != 0 {
			j++
		}
		if j-i > 1 {
			slices.SortStableFunc(cs[i:j], func(a, b char) int { return cmp.Compare(a.ccc, b.ccc) })
		}
		i = j
	}
}

func compose(cs []char) []char {
	out := cs[:0]
	starter := -1
	var last uint8
	for _, c := range cs {
		// Marks kept after the starter are sorted by ccc, so checking the last one is enough.
		if starter >= 0 && (starter == len(out)-1 || last < c.ccc) {
			if p, ok := pair(out[starter].r, c.r); ok {
				out[starter].r = p
				continue
			}
		}
		out = append(out, c)
		if c.ccc == 0 {
			starter = len(out) - 1
		}
		last = c.ccc
	}
	return out
}

func pair(a, b rune) (rune, bool) {
	if l, v := a-lBase, b-vBase; 0 <= l && l < lCount && 0 <= v && v < vCount {
		return sBase + (l*vCount+v)*tCount, true
	}
	if si, t := a-sBase, b-tBase; 0 <= si && si < sCount && si%tCount == 0 && 0 < t && t < tCount {
		return a + t, true
	}
	i, ok := slices.BinarySearchFunc(compositions[:], composition{first: a, second: b}, func(c, t composition) int {
		return cmp.Or(cmp.Compare(c.first, t.first), cmp.Compare(c.second, t.second))
	})
	if !ok {
		return 0, false
	}
	return compositions[i].composite, true
}
