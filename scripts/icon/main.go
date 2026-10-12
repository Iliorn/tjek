// Command icon draws tjek's icon and builds it into the Windows binary.
//
//	go run -C scripts/icon .
//
// The icon is "[✓]" in white, set in JetBrains Mono ExtraBold, with a thin
// dark outline and no background: the mark as a terminal would print it,
// standing on whatever the taskbar or desktop is. The outline is what keeps
// the white legible on a light one. Each size is drawn from the font itself,
// not scaled from another, and the text fills more of the square the smaller
// it gets, so it still reads on a taskbar.
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

// The text is ink, ringed by outline, the Tokyo Night base the icon's tile
// used to be.
var (
	ink     = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	outline = color.NRGBA{0x16, 0x17, 0x21, 0xff}
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
// outline, then the text over it, with the ink centred.
func render(f *opentype.Font, size int) (*image.NRGBA, error) {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	// The outline's width, a pixel at the smallest sizes; the text is sized
	// so text and outline together fill inkFraction of the square.
	r := math.Max(1, math.Round(s/24))

	// Size the face so the ink is that wide, then centre the ink box, not
	// the advance box: the brackets' side bearings would otherwise push it
	// off centre.
	face := func(pt float64) (font.Face, error) {
		return opentype.NewFace(f, &opentype.FaceOptions{Size: pt, DPI: 72, Hinting: font.HintingNone})
	}
	probe, err := face(s)
	if err != nil {
		return nil, err
	}
	b, _ := font.BoundString(probe, text)
	fc, err := face(s * (s*inkFraction(size) - 2*r) / (float64(b.Max.X-b.Min.X) / 64))
	if err != nil {
		return nil, err
	}
	b, _ = font.BoundString(fc, text)
	inkW, inkH := float64(b.Max.X-b.Min.X)/64, float64(b.Max.Y-b.Min.Y)/64
	x := (s-inkW)/2 - float64(b.Min.X)/64
	y := (s-inkH)/2 - float64(b.Min.Y)/64
	mask := image.NewAlpha(img.Bounds())
	d := &font.Drawer{Dst: mask, Src: image.Opaque, Face: fc,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	d.DrawString(text)

	draw.DrawMask(img, img.Bounds(), &image.Uniform{outline}, image.Point{}, dilate(mask, r), image.Point{}, draw.Over)
	draw.DrawMask(img, img.Bounds(), &image.Uniform{ink}, image.Point{}, mask, image.Point{}, draw.Over)
	return img, nil
}

// dilate grows mask by r pixels in every direction: each pixel takes the
// strongest coverage within a disc of radius r around it, which keeps the
// glyphs' anti-aliased edges soft on the outline's edge too.
func dilate(mask *image.Alpha, r float64) *image.Alpha {
	bounds := mask.Bounds()
	out := image.NewAlpha(bounds)
	n := int(math.Ceil(r))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var a uint8
			for dy := -n; dy <= n; dy++ {
				for dx := -n; dx <= n; dx++ {
					if float64(dx*dx+dy*dy) > r*r {
						continue
					}
					if v := mask.AlphaAt(x+dx, y+dy).A; v > a {
						a = v
					}
				}
			}
			out.SetAlpha(x, y, color.Alpha{a})
		}
	}
	return out
}

// inkFraction is how much of the square's width the text and its outline
// fill: more at the small sizes, where every pixel of the glyphs counts.
func inkFraction(size int) float64 {
	switch {
	case size <= 16:
		return 0.92
	case size <= 24:
		return 0.86
	case size <= 32:
		return 0.80
	case size <= 48:
		return 0.74
	}
	return 0.70
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
