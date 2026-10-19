// Command icon draws tjek's icon and builds it into the Windows binary.
//
//	go run -C scripts/icon .
//
// The icon is "[✓]", pale brackets round a green check, set in JetBrains Mono
// ExtraBold, with no outline and no background: the mark as a terminal would
// print it, standing on whatever the taskbar or desktop is. Each glyph is
// lit from above, light at the top and deeper at the foot, and a soft shadow
// falls under the mark, as under Windows 11's own icons; those, not a drawn
// edge, are what lift it off the taskbar. Each size is drawn from the font itself,
// not scaled from another, set tighter than the font's advances so it is
// nearly as tall as it is wide, and fills more of the square the smaller it
// gets, so it stands as large as the square logos beside it on a taskbar.
//
// It writes packaging/icon/tjek.ico and tjek.png, then rsrc_windows_amd64.syso
// in the module root, which `go build` links into a Windows amd64 binary on
// its own, the release's tjek.exe included. Run it again after changing the
// design and commit all three.
//
// The .syso carries the icon and nothing else: no manifest, so Windows runs
// tjek.exe exactly as it did before it had an icon.
//
// The font is fetched from its release on each run and checked against a
// pinned checksum; it is never committed. JetBrains Mono is under the SIL Open
// Font License 1.1, which lets a picture drawn with it be used freely.
//
// This is a module of its own so the font rasterizer it needs is not a
// dependency of tjek.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	text = "[✓]"

	fontZip    = "https://github.com/JetBrains/JetBrainsMono/releases/download/v2.304/JetBrainsMono-2.304.zip"
	fontZipSum = "6f6376c6ed2960ea8a963cd7387ec9d76e3f629125bc33d1fdcd7eb7012f7bbf"
	fontFile   = "fonts/ttf/JetBrainsMono-ExtraBold.ttf"

	// winres builds the .syso; pinned, so a rebuild gives the same file.
	winres = "github.com/tc-hib/go-winres@v0.3.3"

	// root is the module root, from this command's directory.
	root = "../.."
)

// Each glyph runs from its top colour to its foot colour: the brackets from
// white to a pale blue-grey, the check from a light green to a deeper one.
// shadow is the colour under the mark.
var (
	inkTop   = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	inkFoot  = color.NRGBA{0xc8, 0xcf, 0xe6, 0xff}
	tickTop  = color.NRGBA{0xc6, 0xff, 0x8a, 0xff}
	tickFoot = color.NRGBA{0x3f, 0xbf, 0x6a, 0xff}
	shadow   = color.NRGBA{0, 0, 0, 90}
)

// sizes are the sizes Windows asks an icon for, at 100% to 200% scaling.
var sizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "icon:", err)
		os.Exit(1)
	}
}

func run() error {
	ttf, err := fetchFont()
	if err != nil {
		return err
	}
	f, err := opentype.Parse(ttf)
	if err != nil {
		return fmt.Errorf("reading the font: %w", err)
	}
	var images [][]byte
	var largest []byte
	for _, size := range sizes {
		img, err := render(f, size)
		if err != nil {
			return err
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			return err
		}
		largest = b.Bytes()
		// Only the 256 is a PNG in the .ico; the rest are bitmaps. Windows
		// reads a PNG at any size, but the taskbar and the console window
		// draw the transparent pixels of a small PNG frame black, which put
		// the mark on a black square. Bitmaps with an alpha channel are what
		// every part of Windows draws right, and Microsoft's own guidance.
		if size < 256 {
			images = append(images, encodeDIB(img))
		} else {
			images = append(images, largest)
		}
	}
	dir := filepath.Join(root, "packaging", "icon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ico := filepath.Join(dir, "tjek.ico")
	if err := os.WriteFile(ico, encodeICO(sizes, images), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tjek.png"), largest, 0o644); err != nil {
		return err
	}
	cmd := exec.Command("go", "run", winres, "simply",
		"--arch", "amd64", "--manifest", "none", "--icon", ico, "--out", filepath.Join(root, "rsrc"))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// fetchFont downloads the font's release, checks it, and returns the face.
func fetchFont() ([]byte, error) {
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Get(fontZip)
	if err != nil {
		return nil, fmt.Errorf("fetching the font: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the font: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("fetching the font: %w", err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != fontZipSum {
		return nil, fmt.Errorf("the font release does not match its pinned checksum")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, zf := range zr.File {
		if zf.Name != fontFile {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("the font release has no %s", fontFile)
}

// render draws the icon at size pixels square on a transparent ground: the
// shadow, then the text over it, with the ink and its shadow centred.
func render(f *opentype.Font, size int) (*image.NRGBA, error) {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	// How far the shadow falls, a pixel at the smallest sizes; the text is
	// sized so it and its shadow fit the square.
	drop := math.Max(1, s/32)

	// The glyphs are set one by one, ink box to ink box, glyphGap apart,
	// rather than at the font's advances: a monospace bracket sits in a cell
	// as wide as an M, and that air made the mark so much wider than tall
	// that, fitted to the width, it stood at half the height of the square
	// logos beside it on a taskbar. The face is sized so the set ink fills
	// the width or the height, whichever it reaches first, and the ink box,
	// not the advance box, is centred.
	face := func(pt float64) (font.Face, error) {
		return opentype.NewFace(f, &opentype.FaceOptions{Size: pt, DPI: 72, Hinting: font.HintingNone})
	}
	probe, err := face(s)
	if err != nil {
		return nil, err
	}
	w, h, _ := setInk(probe)
	fc, err := face(s * math.Min(s*inkFraction(size)/w, (s*inkHeight-2*drop)/h))
	if err != nil {
		return nil, err
	}
	inkW, inkH, top := setInk(fc)
	x := (s - inkW) / 2
	inkY := (s - inkH - drop) / 2
	y := inkY - top
	// Each glyph goes into the mask of its own colour and into all, which
	// the shadow is cast from.
	all := image.NewAlpha(img.Bounds())
	brackets := image.NewAlpha(img.Bounds())
	tick := image.NewAlpha(img.Bounds())
	for _, ch := range text {
		gb, _ := font.BoundString(fc, string(ch))
		dot := fixed.Point26_6{X: fixed.Int26_6((x - float64(gb.Min.X)/64) * 64), Y: fixed.Int26_6(y * 64)}
		own := brackets
		if ch == '✓' {
			own = tick
		}
		for _, dst := range []*image.Alpha{all, own} {
			d := &font.Drawer{Dst: dst, Src: image.Opaque, Face: fc, Dot: dot}
			d.DrawString(string(ch))
		}
		x += float64(gb.Max.X-gb.Min.X)/64 + glyphGap*inkH
	}

	cast := shift(blur(all, math.Max(1, s/40)), int(math.Round(drop)))
	draw.DrawMask(img, img.Bounds(), &image.Uniform{shadow}, image.Point{}, cast, image.Point{}, draw.Over)
	draw.DrawMask(img, img.Bounds(), lit(img.Bounds(), inkTop, inkFoot, inkY, inkH), image.Point{}, brackets, image.Point{}, draw.Over)
	draw.DrawMask(img, img.Bounds(), lit(img.Bounds(), tickTop, tickFoot, inkY, inkH), image.Point{}, tick, image.Point{}, draw.Over)
	return img, nil
}

// lit is a fill running from top at row y0 to foot at row y0+h, held at
// either colour beyond them.
func lit(bounds image.Rectangle, top, foot color.NRGBA, y0, h float64) *image.NRGBA {
	g := image.NewNRGBA(bounds)
	mix := func(a, b uint8, t float64) uint8 { return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t)) }
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		t := math.Max(0, math.Min(1, (float64(y)+0.5-y0)/h))
		c := color.NRGBA{mix(top.R, foot.R, t), mix(top.G, foot.G, t), mix(top.B, foot.B, t), 0xff}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			g.SetNRGBA(x, y, c)
		}
	}
	return g
}

// glyphGap is the space between one glyph's ink and the next, as a share of
// the mark's height; inkHeight is the most of the square's height the mark
// and its shadow may take.
const (
	glyphGap  = 0.10
	inkHeight = 0.90
)

// setInk is the width and height of text's ink set glyph by glyph, glyphGap
// apart, and its top as an offset from the baseline (negative: above it).
func setInk(fc font.Face) (w, h, top float64) {
	b, _ := font.BoundString(fc, text)
	h, top = float64(b.Max.Y-b.Min.Y)/64, float64(b.Min.Y)/64
	n := 0
	for _, ch := range text {
		gb, _ := font.BoundString(fc, string(ch))
		w += float64(gb.Max.X-gb.Min.X) / 64
		n++
	}
	return w + float64(n-1)*glyphGap*h, h, top
}

// blur softens mask with a box blur of radius r, across and then down.
func blur(mask *image.Alpha, r float64) *image.Alpha {
	n := int(math.Ceil(r))
	bounds := mask.Bounds()
	src := mask
	for _, step := range []image.Point{{1, 0}, {0, 1}} {
		out := image.NewAlpha(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				sum := 0
				for d := -n; d <= n; d++ {
					sum += int(src.AlphaAt(x+d*step.X, y+d*step.Y).A)
				}
				out.SetAlpha(x, y, color.Alpha{uint8(sum / (2*n + 1))})
			}
		}
		src = out
	}
	return src
}

// shift moves mask down by dy pixels.
func shift(mask *image.Alpha, dy int) *image.Alpha {
	bounds := mask.Bounds()
	out := image.NewAlpha(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.SetAlpha(x, y, mask.AlphaAt(x, y-dy))
		}
	}
	return out
}

// inkFraction is how much of the square's width the text fills: more at the small sizes, where every pixel of the glyphs counts.
func inkFraction(size int) float64 {
	switch {
	case size <= 24:
		return 0.98
	case size <= 48:
		return 0.96
	}
	return 0.94
}

// encodeDIB is img as an icon bitmap: a BITMAPINFOHEADER of twice the
// height, for the colour rows and the mask under them; 32-bit BGRA rows
// bottom up, with straight alpha; then the 1-bit AND mask, set where a pixel
// is fully transparent, for whatever still reads it.
func encodeDIB(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	maskStride := (w + 31) / 32 * 4
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	le(uint32(40))                   // biSize
	le(int32(w))                     // biWidth
	le(int32(2 * h))                 // biHeight: colour and mask
	le(uint16(1))                    // biPlanes
	le(uint16(32))                   // biBitCount
	le(uint32(0))                    // biCompression: BI_RGB
	le(uint32(w*h*4 + maskStride*h)) // biSizeImage
	le([4]uint32{0, 0, 0, 0})        // resolution and palette: unused
	for y := h - 1; y >= 0; y-- {
		for x := range w {
			c := img.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, maskStride)
		for x := range w {
			if img.NRGBAAt(x, y).A == 0 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		b.Write(row)
	}
	return b.Bytes()
}

// encodeICO packs images, each a PNG or a bitmap, into an .ico: a header, a
// directory entry per image, then the images. A size of 256 is written as 0,
// as the format has it.
func encodeICO(sizes []int, pngs [][]byte) []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	le([3]uint16{0, 1, uint16(len(pngs))})
	offset := 6 + 16*len(pngs)
	for i, data := range pngs {
		side := uint8(sizes[i] % 256)
		le([4]uint8{side, side, 0, 0})
		le([2]uint16{1, 32}) // planes, bits per pixel
		le([2]uint32{uint32(len(data)), uint32(offset)})
		offset += len(data)
	}
	for _, data := range pngs {
		b.Write(data)
	}
	return b.Bytes()
}
