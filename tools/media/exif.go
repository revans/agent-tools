package main

import "encoding/binary"

// jpegOrientation returns the EXIF orientation (1-8) of a JPEG, or 0 when the
// file has none or the EXIF data is malformed. It never fails: bad EXIF is
// ignored.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 0
		}
		m := b[i+1]
		if m == 0xFF { // fill byte
			i++
			continue
		}
		if m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7) { // no length
			i += 2
			continue
		}
		if m == 0xD9 || m == 0xDA { // end of image, start of scan
			return 0
		}
		n := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if n < 2 || i+2+n > len(b) {
			return 0
		}
		if m == 0xE1 {
			seg := b[i+4 : i+2+n]
			if len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
				if o := tiffOrientation(seg[6:]); o != 0 {
					return o
				}
			}
		}
		i += 2 + n
	}
	return 0
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 0
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off > len(t)-2 {
		return 0
	}
	count := int(bo.Uint16(t[off : off+2]))
	p := off + 2
	for k := 0; k < count; k++ {
		if p+12 > len(t) {
			return 0
		}
		e := t[p : p+12]
		p += 12
		if bo.Uint16(e[0:2]) != 0x0112 {
			continue
		}
		// type SHORT (3), count 1; the value sits in the first two bytes.
		if bo.Uint16(e[2:4]) != 3 || bo.Uint32(e[4:8]) != 1 {
			return 0
		}
		v := int(bo.Uint16(e[8:10]))
		if v < 1 || v > 8 {
			return 0
		}
		return v
	}
	return 0
}
