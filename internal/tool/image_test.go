package tool

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/picture"
)

// size returns the size of the one image a result shows, which is a PNG.
func size(t *testing.T, result Result) (int, int) {
	t.Helper()

	if len(result.Images) != 1 || result.Images[0].MediaType != "image/png" {
		t.Fatalf("images = %d", len(result.Images))
	}

	config, err := png.DecodeConfig(bytes.NewReader(result.Images[0].Data))

	if err != nil {
		t.Fatal(err)
	}

	return config.Width, config.Height
}

func TestReadShowsAnImage(t *testing.T) {
	dir := t.TempDir()

	for name, side := range map[string]int{"small.png": 40, "large.png": 3000} {
		var buffer bytes.Buffer
		png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, side, side/2)))
		os.WriteFile(filepath.Join(dir, name), buffer.Bytes(), 0o644)
	}

	small, err := runTool(t, Read{}, dir, map[string]any{"path": "small.png"})

	if err != nil || small.Text != "[image small.png: png, 40×20]" {
		t.Fatalf("small = %q, %v", small.Text, err)
	}

	if width, height := size(t, small); width != 40 || height != 20 {
		t.Fatalf("small shown at %d×%d", width, height)
	}

	large, err := runTool(t, Read{}, dir, map[string]any{"path": "large.png"})

	if err != nil || !strings.Contains(large.Text, "3000×1500, shown at") {
		t.Fatalf("large = %q, %v", large.Text, err)
	}

	if width, height := size(t, large); width > picture.MaxSide || width*height > picture.MaxPixels {
		t.Fatalf("large shown at %d×%d", width, height)
	}
}

// A PNG some model APIs cannot read, such as a 16-bit gray one, is sent as
// 8-bit color, the same size.
func TestReadSendsAPNGEveryAPIReads(t *testing.T) {
	dir := t.TempDir()
	var buffer bytes.Buffer
	png.Encode(&buffer, image.NewGray16(image.Rect(0, 0, 30, 10)))
	os.WriteFile(filepath.Join(dir, "gray.png"), buffer.Bytes(), 0o644)

	result, err := runTool(t, Read{}, dir, map[string]any{"path": "gray.png"})

	if err != nil || result.Text != "[image gray.png: png, 30×10]" {
		t.Fatalf("result = %q, %v", result.Text, err)
	}

	if width, height := size(t, result); width != 30 || height != 10 {
		t.Fatalf("shown at %d×%d", width, height)
	}

	if sent := result.Images[0].Data; sent[24] != 8 || (sent[25] != 2 && sent[25] != 6) {
		t.Fatalf("sent with depth %d and color type %d", sent[24], sent[25])
	}
}
