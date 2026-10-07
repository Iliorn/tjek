// Command icon draws tjek's icon and builds it into the Windows binary.
//
//	go run ./scripts/icon
//
// The icon is the pixel grid below, a [✓] in the Tokyo Night palette, scaled
// up by whole pixels so it stays crisp at every size Windows asks for. It
// writes packaging/icon/tjek.ico and tjek.png, then rsrc_windows_amd64.syso
// in the module root, which `go build` links into a Windows amd64 binary on
// its own, the release's tjek.exe included. Run it again after changing the
// grid and commit all three.
//
// The .syso carries the icon and nothing else: no manifest, so Windows runs
// tjek.exe exactly as it did before it had an icon.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// logo is the icon, one character a pixel: b the tile, B the brackets, G the
// tick, . transparent.
const logo = `
.bbbbbbbbbbbbbb.
bbbbbbbbbbbbbbbb
bbBBBbbbbbbBBBbb
bbBbbbbbbbbbbBbb
bbBbbbbbbbbbbBbb
bbBbbbbbbbGGbBbb
bbBbbbbbbGGbbBbb
bbBbGbbbGGbbbBbb
bbBbGGbGGbbbbBbb
bbBbbGGGbbbbbBbb
bbBbbbGbbbbbbBbb
bbBbbbbbbbbbbBbb
bbBbbbbbbbbbbBbb
bbBBBbbbbbbBBBbb
bbbbbbbbbbbbbbbb
.bbbbbbbbbbbbbb.
`

var palette = map[byte]color.NRGBA{
	'b': {0x1a, 0x1b, 0x26, 0xff}, // bg
	'B': {0x7a, 0xa2, 0xf7, 0xff}, // blue
	'G': {0x9e, 0xce, 0x6a, 0xff}, // green
}

// sizes are the icon's sizes, each a whole multiple of the grid.
var sizes = []int{16, 32, 48, 64, 128, 256}

// winres builds the .syso; pinned, so a rebuild gives the same file.
const winres = "github.com/tc-hib/go-winres@v0.3.3"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "icon:", err)
		os.Exit(1)
	}
}

func run() error {
	rows := strings.Fields(logo)
	var pngs [][]byte
	for _, size := range sizes {
		img, err := render(rows, size)
		if err != nil {
			return err
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			return err
		}
		pngs = append(pngs, b.Bytes())
	}
	dir := filepath.Join("packaging", "icon")
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
		"--arch", "amd64", "--manifest", "none", "--icon", ico, "--out", "rsrc")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// render draws the grid at size pixels square, each cell size/16 pixels.
func render(rows []string, size int) (*image.NRGBA, error) {
	n := len(rows)
	if size%n != 0 {
		return nil, fmt.Errorf("size %d is not a multiple of the %d-pixel grid", size, n)
	}
	k := size / n
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y, row := range rows {
		if len(row) != n {
			return nil, fmt.Errorf("row %d is %d pixels wide, want %d", y, len(row), n)
		}
		for x := range n {
			c, ok := palette[row[x]]
			if !ok {
				continue
			}
			for dy := range k {
				for dx := range k {
					img.SetNRGBA(x*k+dx, y*k+dy, c)
				}
			}
		}
	}
	return img, nil
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
