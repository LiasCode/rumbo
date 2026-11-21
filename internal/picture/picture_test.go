package picture

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestScalingAveragesThePixelsItCovers(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 4, 2))

	for x := range 2 {
		for y := range 2 {
			source.Pix[y*source.Stride+x*4] = 200
			source.Pix[y*source.Stride+x*4+3] = 255
		}
	}

	scaled := Scale(source, 2, 1).(*image.RGBA)

	if left, right := scaled.RGBAAt(0, 0), scaled.RGBAAt(1, 0); left.R != 200 || right.R != 0 || left.A != 255 {
		t.Fatalf("scaled = %v %v", left, right)
	}

	if width, height := Fit(1920, 1080, 1280, 800, 0); width != 1280 || height != 720 {
		t.Fatalf("fit = %d×%d", width, height)
	}

	if width, height := Fit(800, 600, 1280, 800, 0); width != 800 || height != 600 {
		t.Fatalf("a small one grew to %d×%d", width, height)
	}
}

// A PNG some model APIs cannot read, such as a 16-bit gray one, is sent as
// 8-bit color, the same size; one every API reads goes as it is.
func TestEveryAPIReadsWhatIsPrepared(t *testing.T) {
	var gray, color bytes.Buffer
	png.Encode(&gray, image.NewGray16(image.Rect(0, 0, 30, 10)))
	png.Encode(&color, image.NewRGBA(image.Rect(0, 0, 30, 10)))

	if readable(gray.Bytes(), "image/png") || !readable(color.Bytes(), "image/png") {
		t.Fatal("readable is wrong")
	}

	shown, about, err := Prepare(gray.Bytes(), Kind(gray.Bytes()))

	if err != nil || about != "png, 30×10" || shown.Data[24] != 8 || (shown.Data[25] != 2 && shown.Data[25] != 6) {
		t.Fatalf("prepared %q, %v, depth %d, color type %d", about, err, shown.Data[24], shown.Data[25])
	}

	if same, _, _ := Prepare(color.Bytes(), "image/png"); !bytes.Equal(same.Data, color.Bytes()) {
		t.Fatal("a readable PNG was encoded again")
	}

	if Kind([]byte("not an image")) != "" {
		t.Fatal("text taken for an image")
	}

	if _, _, err := Prepare([]byte("\x89PNG\r\n\x1a\nbroken"), "image/png"); err == nil || !strings.Contains(err.Error(), "cannot be decoded") {
		t.Fatalf("a broken PNG: %v", err)
	}
}
