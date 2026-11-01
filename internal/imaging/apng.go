package imaging

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"slices"
	"strings"
)

// apngChunks are the chunks that filterAPNG keeps: those of the image and the animation, and those of its colors.
var apngChunks = []string{
	"IHDR", "PLTE", "tRNS", "acTL", "fcTL", "IDAT", "fdAT", "IEND",
	"iCCP", "sRGB", "gAMA", "cHRM", "cICP", "mDCV", "cLLI", "sBIT",
}

// filterAPNG returns the PNG file in data with only apngChunks, up to IEND, each checked against its CRC and copied as
// it is. An unknown critical chunk is an error, as the image may not show without it.
func filterAPNG(data []byte) ([]byte, error) {
	if !strings.HasPrefix(string(data), pngSignature) {
		return nil, errors.New("not a PNG file")
	}
	out := append(make([]byte, 0, len(data)), pngSignature...)
	for i := len(pngSignature); ; {
		if len(data)-i < 12 || uint64(binary.BigEndian.Uint32(data[i:])) > uint64(len(data)-i-12) {
			return nil, errors.New("truncated PNG")
		}
		n := int(binary.BigEndian.Uint32(data[i:]))
		chunk := data[i : i+12+n]
		i += 12 + n
		typ := string(chunk[4:8])
		for _, c := range []byte(typ) {
			if c|0x20 < 'a' || c|0x20 > 'z' {
				return nil, fmt.Errorf("invalid PNG chunk type %q", typ)
			}
		}
		if !slices.Contains(apngChunks, typ) {
			if typ[0]&0x20 == 0 {
				return nil, fmt.Errorf("unknown critical PNG chunk %q", typ)
			}
			continue
		}
		if crc32.ChecksumIEEE(chunk[4:8+n]) != binary.BigEndian.Uint32(chunk[8+n:]) {
			return nil, fmt.Errorf("PNG chunk %q with a wrong CRC", typ)
		}
		out = append(out, chunk...)
		if typ == "IEND" {
			return out, nil
		}
	}
}
