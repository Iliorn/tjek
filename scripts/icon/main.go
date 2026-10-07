// Command icon draws tjek's icon and builds it into the Windows binary.
//
//	go run -C scripts/icon .
//
// The icon is "[✓]" in white, set in JetBrains Mono ExtraBold, on a dark
// rounded tile in the Tokyo Night base: the mark as a terminal would print
// it. Each size is drawn from the font itself, not scaled from another, and
// the text fills more of the tile the smaller it gets, so it still reads on
// a taskbar.
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

// The tile runs from tileTop to tileBottom down its height; the text is ink.
var (
	tileTop    = color.NRGBA{0x24, 0x28, 0x3b, 0xff}
	tileBottom = color.NRGBA{0x16, 0x17, 0x21, 0xff}
	ink        = color.NRGBA{0xff, 0xff, 0xff, 0xff}
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
	var pngs [][]byte
	for _, size := range sizes {
		img, err := render(f, size)
		if err != nil {
			return err
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			return err
		}
		pngs = append(pngs, b.Bytes())
	}
	dir := filepath.Join(root, "packaging", "icon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ico := filepath.Join(dir, "tjek.ico")
	if err := os.WriteFile(ico, encodeICO(sizes, pngs), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tjek.png"), pngs[len(pngs)-1], 0o644); err != nil {
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

// render draws the icon at size pixels square: the tile, then the text with
// its ink centred.
func render(f *opentype.Font, size int) (*image.NRGBA, error) {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	r := 0.22 * s // corner radius
	for y := range size {
		row := lerp(tileTop, tileBottom, float64(y)/s)
		for x := range size {
			// Coverage of the rounded square, from 4x4 samples per pixel.
			cov := 0
			for sy := range 4 {
				for sx := range 4 {
					px, py := float64(x)+(float64(sx)+0.5)/4, float64(y)+(float64(sy)+0.5)/4
					dx := math.Max(0, math.Max(r-px, px-(s-r)))
					dy := math.Max(0, math.Max(r-py, py-(s-r)))
					if dx*dx+dy*dy <= r*r {
						cov++
					}
				}
			}
			if cov > 0 {
				c := row
				c.A = uint8(255 * cov / 16)
				img.SetNRGBA(x, y, c)
			}
		}
	}

	// Size the face so the ink is inkFraction of the tile wide, then centre
	// the ink box, not the advance box: the brackets' side bearings would
	// otherwise push it off centre.
	face := func(pt float64) (font.Face, error) {
		return opentype.NewFace(f, &opentype.FaceOptions{Size: pt, DPI: 72, Hinting: font.HintingNone})
	}
	probe, err := face(s)
	if err != nil {
		return nil, err
	}
	b, _ := font.BoundString(probe, text)
	fc, err := face(s * inkFraction(size) * s / (float64(b.Max.X-b.Min.X) / 64))
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
	draw.DrawMask(img, img.Bounds(), &image.Uniform{ink}, image.Point{}, mask, image.Point{}, draw.Over)
	return img, nil
}

// inkFraction is how much of the tile's width the text fills: more at the
// small sizes, where every pixel of the glyphs counts.
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

func lerp(a, b color.NRGBA, t float64) color.NRGBA {
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.NRGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 0xff}
}

// encodeICO packs PNG images into an .ico: a header, a directory entry per
// image, then the images. A size of 256 is written as 0, as the format has it.
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
