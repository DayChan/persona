package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	thumbMaxSource = 64 << 20  // bigger images are not decoded here (a system tool may still draw them)
	thumbMaxPixels = 100 << 20 // decoded size bound: width × height
	thumbMaxSVG    = 4 << 20
	thumbTimeout   = 25 * time.Second // the hub waits 30
	thumbCacheSize = 128
	// Bump when processing changes so clients revalidate thumbnails drawn by an older node.
	thumbProcessingVersion = "v2"
)

// thumbs keeps the last thumbnails drawn: a file list asks for the same ones each time it scrolls past them.
type thumbs struct {
	mu    sync.Mutex
	order []string
	byKey map[string]*proto.FileThumb
}

var thumbCache = &thumbs{byKey: map[string]*proto.FileThumb{}}

func (c *thumbs) get(key string) *proto.FileThumb {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byKey[key]
}

func (c *thumbs) put(key string, t *proto.FileThumb) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byKey[key]; ok {
		return
	}
	if len(c.order) >= thumbCacheSize {
		delete(c.byKey, c.order[0])
		c.order = c.order[1:]
	}
	c.order = append(c.order, key)
	c.byKey[key] = t
}

// fileThumb draws a file's thumbnail: images decoded here, anything else by the system's tools (Quick Look on a
// Mac: PDF, Office, iWork, HEIC, video; pdftoppm / ffmpeg elsewhere when installed). One version of a file at one
// size is drawn once (thumbCache) and named by its ETag.
func (n *Node) fileThumb(ctx context.Context, p proto.FileThumbRequest) (*proto.FileThumb, error) {
	size := p.Size
	if size == 0 {
		size = proto.ThumbDefault
	}
	size = max(proto.ThumbMin, min(size, proto.ThumbMax))
	path, f, st, err := n.openRegular(p.Path, p.Cwd)
	if err != nil {
		return nil, err
	}
	head := readHead(f)
	f.Close()
	etag := fmt.Sprintf("%s-%s-%d", thumbProcessingVersion, fileRevision(st), size)
	if p.ETag == etag {
		return &proto.FileThumb{ETag: etag, NotModified: true}, nil
	}
	key := path + "\x00" + etag
	if t := thumbCache.get(key); t != nil {
		return t, nil
	}
	ctx, cancel := context.WithTimeout(ctx, thumbTimeout)
	defer cancel()
	m := fileMime(path, head)
	var t *proto.FileThumb
	switch {
	case m == "image/svg+xml" && st.Size() <= thumbMaxSVG:
		t, _ = svgThumb(path, size)
	case strings.HasPrefix(m, "image/") && st.Size() <= thumbMaxSource:
		t, _ = decodeThumb(path, size)
	}
	if t == nil {
		t = toolThumb(ctx, path, m, size)
	}
	if t == nil {
		return nil, &rpc.Error{Code: proto.CodeNoThumbnail, Message: "no thumbnail for " + filepath.Base(path)}
	}
	t.ETag = etag
	thumbCache.put(key, t)
	return t, nil
}

// decodeThumb scales an image Go can decode (PNG, JPEG, GIF, WebP, BMP, TIFF) to fit size.
func decodeThumb(path string, size int) (*proto.FileThumb, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbMaxPixels {
		return nil, fmt.Errorf("image is %d×%d", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	orientation := 1
	if format == "jpeg" {
		orientation = jpegThumbOrientation(f)
		if _, err := f.Seek(0, 0); err != nil {
			return nil, err
		}
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	if orientation >= 2 && orientation <= 8 {
		src = orientedThumb{src, orientation}
	}
	return encodeThumb(src, size)
}

// svgThumb draws an SVG (no scripts, no external references: oksvg only paints shapes) into size × size, keeping
// its aspect ratio from the viewBox.
func svgThumb(path string, size int) (t *proto.FileThumb, err error) {
	defer func() {
		if r := recover(); r != nil {
			t, err = nil, fmt.Errorf("svg: %v", r)
		}
	}()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	icon, err := oksvg.ReadIconStream(bytes.NewReader(data), oksvg.IgnoreErrorMode)
	if err != nil {
		return nil, err
	}
	w, h := size, size
	if vw, vh := icon.ViewBox.W, icon.ViewBox.H; vw > 0 && vh > 0 {
		if vw >= vh {
			h = max(1, int(float64(size)*vh/vw))
		} else {
			w = max(1, int(float64(size)*vw/vh))
		}
	}
	icon.SetTarget(0, 0, float64(w), float64(h))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	icon.Draw(rasterx.NewDasher(w, h, rasterx.NewScannerGV(w, h, img, img.Bounds())), 1)
	return encodeThumb(img, size)
}

// encodeThumb fits img into size × size (never enlarging): JPEG when it is opaque, PNG to keep transparency.
func encodeThumb(img image.Image, size int) (*proto.FileThumb, error) {
	o, ok := img.(interface{ Opaque() bool })
	opaque := ok && o.Opaque() // of the source: scaling may leave an edge pixel a hair short of opaque
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > size || h > size {
		if w >= h {
			w, h = size, max(1, h*size/b.Dx())
		} else {
			w, h = max(1, w*size/b.Dy()), size
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if opaque {
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
			return nil, err
		}
		return &proto.FileThumb{Mime: "image/jpeg", Data: buf.Bytes()}, nil
	}
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &proto.FileThumb{Mime: "image/png", Data: buf.Bytes()}, nil
}

// tools runs one system tool at a time (a file list asks for dozens of thumbnails at once), and after one timed out
// (Quick Look hangs while the Mac is locked) skips them for toolBackoff rather than making every request wait.
var tools = struct {
	sem          chan struct{}
	mu           sync.Mutex
	blockedUntil time.Time
}{sem: make(chan struct{}, 1)}

const toolBackoff = 5 * time.Minute

var toolTimeout = 12 * time.Second // a var for tests

var runCmd = func(cmd *exec.Cmd) error { return cmd.Run() } // a var for tests: they count the tools started

// toolThumb asks the system to draw the file into a temporary folder: Quick Look on a Mac, else pdftoppm for a
// PDF and ffmpeg for a video when the machine has them. The picture it leaves is scaled again to size.
func toolThumb(ctx context.Context, path, mime string, size int) *proto.FileThumb {
	select {
	case tools.sem <- struct{}{}:
		defer func() { <-tools.sem }()
	case <-ctx.Done():
		return nil
	}
	tools.mu.Lock()
	blocked := time.Now().Before(tools.blockedUntil)
	tools.mu.Unlock()
	if blocked {
		return nil
	}
	dir, err := os.MkdirTemp("", "persona-thumb-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	out := ""
	switch {
	case runtime.GOOS == "darwin":
		if runTool(ctx, "qlmanage", "-t", "-s", fmt.Sprint(size), "-o", dir, path) {
			out = filepath.Join(dir, filepath.Base(path)+".png")
		}
	case mime == "application/pdf":
		if runTool(ctx, "pdftoppm", "-png", "-singlefile", "-f", "1", "-l", "1", "-scale-to", fmt.Sprint(size), path, filepath.Join(dir, "page")) {
			out = filepath.Join(dir, "page.png")
		}
	case strings.HasPrefix(mime, "video/"):
		if runTool(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-ss", "1", "-i", path, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", size, size), filepath.Join(dir, "frame.png")) {
			out = filepath.Join(dir, "frame.png")
		}
	}
	if out == "" {
		return nil
	}
	t, err := decodeThumb(out, size)
	if err != nil {
		return nil
	}
	return t
}

// runTool runs a system tool if the machine has it; true when it succeeded.
func runTool(ctx context.Context, name string, args ...string) bool {
	bin, err := exec.LookPath(name)
	if err != nil {
		return false
	}
	tctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, bin, args...)
	cmd.WaitDelay = time.Second
	err = runCmd(cmd)
	if ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) { // the tool hung, not the request gone
		tools.mu.Lock()
		tools.blockedUntil = time.Now().Add(toolBackoff)
		tools.mu.Unlock()
	}
	return err == nil
}
