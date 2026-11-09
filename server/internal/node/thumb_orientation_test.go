package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

var thumbCornerColors = [4]color.NRGBA{
	{220, 30, 40, 255}, {30, 210, 50, 255}, {30, 50, 220, 255}, {220, 210, 30, 255},
}

// Distinct quadrants reveal mirrors as well as rotations, away from JPEG edge artifacts.
func cornerJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			corner := 0
			if x >= w/2 {
				corner++
			}
			if y >= h/2 {
				corner += 2
			}
			img.SetNRGBA(x, y, thumbCornerColors[corner])
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func thumbEXIF(order binary.ByteOrder, orientation uint16) []byte {
	data := make([]byte, 32)
	copy(data, "Exif\x00\x00")
	tiff := data[6:]
	if order == binary.LittleEndian {
		copy(tiff, "II")
	} else {
		copy(tiff, "MM")
	}
	order.PutUint16(tiff[2:], 42)
	order.PutUint32(tiff[4:], 8)
	order.PutUint16(tiff[8:], 1)
	order.PutUint16(tiff[10:], 0x0112)
	order.PutUint16(tiff[12:], 3) // SHORT
	order.PutUint32(tiff[14:], 1)
	order.PutUint16(tiff[18:], orientation)
	return data
}

func jpegWithSegments(jpg []byte, segments ...[]byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	for _, segment := range segments {
		out = append(out, 0xff, 0xe1, byte((len(segment)+2)>>8), byte(len(segment)+2))
		out = append(out, segment...)
	}
	return append(out, jpg[2:]...)
}

func assertThumbCorners(t *testing.T, th *proto.FileThumb, w, h int, corners [4]int) {
	t.Helper()
	if th == nil || th.Mime != "image/jpeg" || th.NotModified || len(th.Data) == 0 {
		t.Fatalf("expected a JPEG thumbnail, got %+v", th)
	}
	img, err := jpeg.Decode(bytes.NewReader(th.Data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("thumbnail is %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	for i, p := range []image.Point{{w / 4, h / 4}, {3 * w / 4, h / 4}, {w / 4, 3 * h / 4}, {3 * w / 4, 3 * h / 4}} {
		got := color.NRGBAModel.Convert(img.At(p.X, p.Y)).(color.NRGBA)
		want := thumbCornerColors[corners[i]]
		if abs(int(got.R)-int(want.R)) > 20 || abs(int(got.G)-int(want.G)) > 20 || abs(int(got.B)-int(want.B)) > 20 {
			t.Errorf("corner %d = %v, want near %v", i, got, want)
		}
	}
}

func TestThumbnailJPEGOrientations(t *testing.T) {
	corners := [8][4]int{
		{0, 1, 2, 3}, {1, 0, 3, 2}, {3, 2, 1, 0}, {2, 3, 0, 1},
		{0, 2, 1, 3}, {2, 0, 3, 1}, {3, 1, 2, 0}, {1, 3, 0, 2},
	}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for orientation := 1; orientation <= 8; orientation++ {
			for _, scaled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/scaled=%t", order, orientation, scaled), func(t *testing.T) {
					dir := t.TempDir()
					n := &Node{home: dir}
					w, h, size := 60, 30, 128
					if scaled {
						w, h, size = 240, 120, 64
					}
					source := jpegWithSegments(cornerJPEG(t, w, h), thumbEXIF(order, uint16(orientation)))
					path := filepath.Join(dir, "corners.jpg")
					if err := os.WriteFile(path, source, 0600); err != nil {
						t.Fatal(err)
					}
					before, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					th, err := n.fileThumb(context.Background(), proto.FileThumbRequest{Path: path, Size: size})
					if err != nil {
						t.Fatal(err)
					}
					if scaled {
						w, h = 64, 32
					}
					if orientation >= 5 {
						w, h = h, w
					}
					assertThumbCorners(t, th, w, h, corners[orientation-1])
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(after, source) {
						t.Fatalf("source bytes changed: %v", err)
					}
					st, err := os.Stat(path)
					if err != nil || st.Size() != before.Size() || st.Mode() != before.Mode() || !st.ModTime().Equal(before.ModTime()) {
						t.Fatalf("source metadata changed: %v", err)
					}
				})
			}
		}
	}
}

func TestThumbnailProcessingVersionInvalidatesOldETags(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	path := filepath.Join(dir, "portrait.jpg")
	source := jpegWithSegments(cornerJPEG(t, 60, 30), thumbEXIF(binary.LittleEndian, 6))
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	oldETag := fmt.Sprintf("%s-%d", fileRevision(st), 128)
	old := &proto.FileThumb{ETag: oldETag, Mime: "image/jpeg", Data: []byte("old sideways thumbnail")}
	thumbCache.put(path+"\x00"+oldETag, old)
	request := proto.FileThumbRequest{Path: path, Size: 128, ETag: oldETag}
	th, err := n.fileThumb(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertThumbCorners(t, th, 30, 60, [4]int{2, 0, 3, 1})
	if th.ETag == "" || th.ETag == oldETag {
		t.Fatalf("processing change kept the old ETag: %q", th.ETag)
	}
	request.ETag = th.ETag
	conditional, err := n.fileThumb(context.Background(), request)
	if err != nil || !conditional.NotModified || len(conditional.Data) != 0 || conditional.ETag != th.ETag {
		t.Fatalf("current validator = %+v, %v", conditional, err)
	}
	for _, etag := range []string{"", oldETag, "opaque-unrelated-validator"} {
		request.ETag = etag
		again, err := n.fileThumb(context.Background(), request)
		if err != nil || again.NotModified || again.ETag != th.ETag || !bytes.Equal(again.Data, th.Data) {
			t.Fatalf("unmatched validator %q = %+v, %v", etag, again, err)
		}
	}
	request.Size, request.ETag = 32, th.ETag
	small, err := n.fileThumb(context.Background(), request)
	if err != nil || small.ETag == th.ETag {
		t.Fatalf("different size reused validator: %+v, %v", small, err)
	}
	assertThumbCorners(t, small, 16, 32, [4]int{2, 0, 3, 1})
}

func TestThumbnailJPEGFailureCanBeRetried(t *testing.T) {
	// No system thumbnail tool may turn a corrupt JPEG into a platform-specific icon.
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	n := &Node{home: dir}
	path := filepath.Join(dir, "retry.jpg")
	source := jpegWithSegments(cornerJPEG(t, 60, 30), thumbEXIF(binary.BigEndian, 8))
	broken := bytes.Clone(source)
	broken[0] = 0 // same size and revision as the successful retry below
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	request := proto.FileThumbRequest{Path: path, Size: 128}
	_, err = n.fileThumb(context.Background(), request)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeNoThumbnail {
		t.Fatalf("corrupt JPEG: %v", err)
	}
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || fileRevision(after) != fileRevision(before) {
		t.Fatalf("retry requires the same source revision: %v", err)
	}
	th, err := n.fileThumb(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertThumbCorners(t, th, 30, 60, [4]int{1, 3, 0, 2})

	// Concurrent requests, including another source at the same size, remain independent.
	otherPath := filepath.Join(dir, "other.jpg")
	if err := os.WriteFile(otherPath, jpegWithSegments(cornerJPEG(t, 60, 30), thumbEXIF(binary.BigEndian, 2)), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(otherPath, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	otherStat, err := os.Stat(otherPath)
	if err != nil || fileRevision(otherStat) != fileRevision(st) {
		t.Fatalf("test requires equal source revisions: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			p, w, h, corners := path, 30, 60, [4]int{1, 3, 0, 2}
			if i%2 == 1 {
				p, w, h, corners = otherPath, 60, 30, [4]int{1, 0, 3, 2}
			}
			got, err := n.fileThumb(context.Background(), proto.FileThumbRequest{Path: p, Size: 128})
			if err != nil {
				t.Error(err)
				return
			}
			assertThumbCorners(t, got, w, h, corners)
		})
	}
	wg.Wait()
}

func TestThumbnailJPEGMetadataFallback(t *testing.T) {
	jpg := cornerJPEG(t, 60, 30)
	mutate := func(edit func([]byte)) []byte {
		data := thumbEXIF(binary.LittleEndian, 6)
		edit(data[6:])
		return data
	}
	cases := map[string][]byte{
		"no metadata":         nil,
		"empty APP1":          {},
		"XMP APP1":            []byte("http://ns.adobe.com/xap/1.0/\x00"),
		"short EXIF":          []byte("Exif\x00\x00II"),
		"bad byte order":      mutate(func(d []byte) { copy(d[:2], "XX") }),
		"bad TIFF magic":      mutate(func(d []byte) { binary.LittleEndian.PutUint16(d[2:], 43) }),
		"IFD in header":       mutate(func(d []byte) { binary.LittleEndian.PutUint32(d[4:], 2) }),
		"IFD past end":        mutate(func(d []byte) { binary.LittleEndian.PutUint32(d[4:], 0xffffffff) }),
		"too many entries":    mutate(func(d []byte) { binary.LittleEndian.PutUint16(d[8:], 0xffff) }),
		"wrong tag":           mutate(func(d []byte) { binary.LittleEndian.PutUint16(d[10:], 0x0113) }),
		"wrong type":          mutate(func(d []byte) { binary.LittleEndian.PutUint16(d[12:], 4) }),
		"multiple values":     mutate(func(d []byte) { binary.LittleEndian.PutUint32(d[14:], 2) }),
		"huge value count":    mutate(func(d []byte) { binary.LittleEndian.PutUint32(d[14:], 0xffffffff) }),
		"zero orientation":    thumbEXIF(binary.LittleEndian, 0),
		"unknown orientation": thumbEXIF(binary.BigEndian, 9),
		"huge orientation":    thumbEXIF(binary.BigEndian, 0xffff),
		"truncated IFD":       thumbEXIF(binary.LittleEndian, 6)[:27],
	}
	// A cyclic next-IFD pointer must not be traversed, nor may the thumbnail's orientation be used.
	thumbnailOnly := mutate(func(d []byte) {
		binary.LittleEndian.PutUint16(d[8:], 0)
		binary.LittleEndian.PutUint32(d[10:], 8)
	})
	cases["cyclic IFD"] = thumbnailOnly
	for name, metadata := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "fallback.jpg")
			source := jpg
			if metadata != nil {
				source = jpegWithSegments(jpg, metadata)
			}
			if err := os.WriteFile(path, source, 0600); err != nil {
				t.Fatal(err)
			}
			th, err := (&Node{home: dir}).fileThumb(context.Background(), proto.FileThumbRequest{Path: path, Size: 128})
			if err != nil {
				t.Fatal(err)
			}
			assertThumbCorners(t, th, 60, 30, [4]int{0, 1, 2, 3})
			raw, err := jpeg.Decode(bytes.NewReader(jpg))
			if err != nil {
				t.Fatal(err)
			}
			want, err := encodeThumb(raw, 128)
			if err != nil || !bytes.Equal(th.Data, want.Data) {
				t.Fatalf("fallback changed normal JPEG output: %v", err)
			}
		})
	}
}

func TestJPEGThumbOrientationHeaderBoundaries(t *testing.T) {
	valid := thumbEXIF(binary.LittleEndian, 6)
	jpg := cornerJPEG(t, 60, 30)
	for n := 0; n < len(valid); n++ {
		if got := jpegThumbOrientation(bytes.NewReader(jpegWithSegments(jpg, valid[:n]))); got != 1 {
			t.Fatalf("truncated EXIF length %d = %d", n, got)
		}
	}
	for _, data := range [][]byte{
		nil, {0xff}, {0xff, 0xd8, 0xff, 0xe1}, {0xff, 0xd8, 0xff, 0xe1, 0, 1},
		{0xff, 0xd8, 0xff, 0xe1, 0, 40}, {0xff, 0xd8, 0, 0},
		append([]byte{0xff, 0xd8, 0xff, 0xda}, jpegWithSegments(jpg, valid)[2:]...),
		append([]byte{0xff, 0xd8, 0xff, 0xd9}, jpegWithSegments(jpg, valid)[2:]...),
	} {
		if got := jpegThumbOrientation(bytes.NewReader(data)); got != 1 {
			t.Fatalf("bad header or EXIF after scan/end marker = %d", got)
		}
	}
	// APP1 may contain XMP, and EXIF may appear after more than one maximum-size segment.
	late := jpegWithSegments(jpg, make([]byte, 65533), make([]byte, 65533), valid)
	if got := jpegThumbOrientation(bytes.NewReader(late)); got != 6 {
		t.Fatalf("late EXIF = %d", got)
	}
	filled := append([]byte{0xff, 0xd8, 0xff}, jpegWithSegments(jpg, valid)[2:]...)
	if got := jpegThumbOrientation(bytes.NewReader(filled)); got != 6 {
		t.Fatalf("marker fill bytes = %d", got)
	}
	// IFD0 need not immediately follow the TIFF header; unrelated entries may precede orientation.
	relocated := append([]byte{}, valid[:14]...)
	relocated = append(relocated, make([]byte, 8)...)
	relocated = append(relocated, 2, 0)
	relocated = append(relocated, make([]byte, 12)...)
	relocated = append(relocated, valid[16:]...)
	binary.LittleEndian.PutUint32(relocated[10:], 16)
	if got := jpegThumbOrientation(bytes.NewReader(jpegWithSegments(jpg, relocated))); got != 6 {
		t.Fatalf("relocated IFD with unrelated entry = %d", got)
	}
	probe := &thumbMarkerFillReader{}
	if got := jpegThumbOrientation(io.MultiReader(bytes.NewReader([]byte{0xff, 0xd8}), probe)); got != 1 || probe.read > thumbMaxSource-2 {
		t.Fatalf("unending header = %d, read %d bytes", got, probe.read)
	}
}

type thumbMarkerFillReader struct{ read int }

func (r *thumbMarkerFillReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0xff
	}
	r.read += len(p)
	return len(p), nil
}

func TestThumbnailJPEGSourceLimit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "bounded.jpg")
	if err := os.WriteFile(path, jpegWithSegments(cornerJPEG(t, 60, 30), thumbEXIF(binary.LittleEndian, 6)), 0600); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: dir}
	request := proto.FileThumbRequest{Path: path, Size: 128}
	if err := os.Truncate(path, thumbMaxSource+1); err != nil {
		t.Fatal(err)
	}
	_, err := n.fileThumb(context.Background(), request)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeNoThumbnail {
		t.Fatalf("oversized JPEG: %v", err)
	}
	if err := os.Truncate(path, thumbMaxSource); err != nil {
		t.Fatal(err)
	}
	th, err := n.fileThumb(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertThumbCorners(t, th, 30, 60, [4]int{2, 0, 3, 1})
}

func TestThumbnailJPEGPixelLimit(t *testing.T) {
	data := jpegWithSegments(cornerJPEG(t, 60, 30), thumbEXIF(binary.LittleEndian, 6))
	sof := bytes.Index(data, []byte{0xff, 0xc0})
	if sof < 0 {
		t.Fatal("fixture has no baseline JPEG frame header")
	}
	binary.BigEndian.PutUint16(data[sof+5:], 10240)
	binary.BigEndian.PutUint16(data[sof+7:], 10241) // just above thumbMaxPixels
	path := filepath.Join(t.TempDir(), "too-many-pixels.jpg")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeThumb(path, 128); err == nil || err.Error() != "image is 10241×10240" {
		t.Fatalf("pixel bound was not checked before decoding: %v", err)
	}
}

func FuzzJPEGThumbOrientation(f *testing.F) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for orientation := uint16(0); orientation <= 9; orientation++ {
			f.Add(jpegWithSegments([]byte{0xff, 0xd8, 0xff, 0xd9}, thumbEXIF(order, orientation)))
		}
	}
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		if got := jpegThumbOrientation(bytes.NewReader(data)); got < 1 || got > 8 {
			t.Fatalf("invalid orientation %d", got)
		}
	})
}
