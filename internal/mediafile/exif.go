package mediafile

import "encoding/binary"

// exifHeader is the Exif ID code before the TIFF header in the APP1 segment of JPEG (CIPA DC-008 4.7.2).
const exifHeader = "Exif\x00\x00"

// orientation returns the Orientation (tag 0x0112) in IFD0 of tiff, Exif data that starts with the TIFF header.
// It returns 1 unless the first entry with the tag has 1 SHORT from 1 to 8. The other IFDs are not read.
func orientation(tiff []byte) int {
	var order binary.ByteOrder
	switch {
	case len(tiff) < 8:
		return 1
	case string(tiff[:4]) == "II*\x00":
		order = binary.LittleEndian
	case string(tiff[:4]) == "MM\x00*":
		order = binary.BigEndian
	default:
		return 1
	}
	off := int64(order.Uint32(tiff[4:]))
	if off < 8 || off+2 > int64(len(tiff)) {
		return 1
	}
	n := order.Uint16(tiff[off:])
	// Only the entries that fit, as tiff may be cut at the end of what Probe reads.
	for e := tiff[off+2:]; n > 0 && len(e) >= 12; n, e = n-1, e[12:] {
		if order.Uint16(e) != 0x0112 {
			continue
		}
		// A SHORT is in the first 2 bytes of the value field.
		if v := order.Uint16(e[8:]); order.Uint16(e[2:]) == 3 && order.Uint32(e[4:]) == 1 && 1 <= v && v <= 8 {
			return int(v)
		}
		return 1
	}
	return 1
}
