package node

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"io"
)

// jpegThumbOrientation reads headers only, stopping before compressed pixels. Each APP1 is at most
// 65533 bytes, and the complete scan shares the thumbnail source-size bound. Bad metadata means identity.
func jpegThumbOrientation(src io.Reader) int {
	r := bufio.NewReader(io.LimitReader(src, thumbMaxSource))
	var pair [2]byte
	if _, err := io.ReadFull(r, pair[:]); err != nil || pair != [2]byte{0xff, 0xd8} {
		return 1
	}
	for {
		prefix, err := r.ReadByte()
		if err != nil || prefix != 0xff {
			return 1
		}
		marker, err := r.ReadByte()
		for marker == 0xff && err == nil { // marker fill bytes
			marker, err = r.ReadByte()
		}
		if err != nil || marker == 0 || marker == 0xd8 || marker == 0xda || marker == 0xd9 {
			return 1
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 { // standalone markers
			continue
		}
		if _, err := io.ReadFull(r, pair[:]); err != nil {
			return 1
		}
		n := int(binary.BigEndian.Uint16(pair[:])) - 2
		if n < 0 {
			return 1
		}
		if marker == 0xe1 {
			data := make([]byte, n)
			if _, err := io.ReadFull(r, data); err != nil {
				return 1
			}
			if orientation := exifThumbOrientation(data); orientation != 0 {
				return orientation
			}
		} else if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
			return 1
		}
	}
}

// exifThumbOrientation reads only IFD0's inline SHORT orientation. Never follow EXIF subdirectories or
// the embedded thumbnail's IFD: their offsets may be cyclic, and their orientation is not the main image's.
func exifThumbOrientation(data []byte) int {
	if len(data) < 14 || !bytes.Equal(data[:6], []byte("Exif\x00\x00")) {
		return 0
	}
	tiff := data[6:]
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 0
	}
	offset := uint64(order.Uint32(tiff[4:8]))
	if offset < 8 || offset+2 > uint64(len(tiff)) {
		return 0
	}
	ifd := tiff[int(offset):]
	count := int(order.Uint16(ifd[:2]))
	if len(ifd) < 6 || count > (len(ifd)-6)/12 { // entries and the next-IFD pointer must fit
		return 0
	}
	for i := 0; i < count; i++ {
		entry := ifd[2+i*12 : 14+i*12]
		if order.Uint16(entry[:2]) != 0x0112 {
			continue
		}
		if order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return 0
		}
		value := int(order.Uint16(entry[8:10]))
		if value >= 1 && value <= 8 {
			return value
		}
		return 0
	}
	return 0
}

// orientedThumb presents display coordinates to the scaler without allocating another full-size image.
type orientedThumb struct {
	image.Image
	orientation int
}

func (img orientedThumb) Bounds() image.Rectangle {
	b := img.Image.Bounds()
	w, h := b.Dx(), b.Dy()
	if img.orientation >= 5 {
		w, h = h, w
	}
	return image.Rect(0, 0, w, h)
}

func (img orientedThumb) At(x, y int) color.Color {
	b := img.Image.Bounds()
	w, h := b.Dx(), b.Dy()
	switch img.orientation {
	case 2: // horizontal mirror
		x = w - 1 - x
	case 3: // half turn
		x, y = w-1-x, h-1-y
	case 4: // vertical mirror
		y = h - 1 - y
	case 5: // transpose
		x, y = y, x
	case 6: // clockwise quarter turn
		x, y = y, h-1-x
	case 7: // transverse
		x, y = w-1-y, h-1-x
	case 8: // counterclockwise quarter turn
		x, y = w-1-y, x
	}
	return img.Image.At(b.Min.X+x, b.Min.Y+y)
}

func (img orientedThumb) Opaque() bool {
	o, ok := img.Image.(interface{ Opaque() bool })
	return ok && o.Opaque()
}
