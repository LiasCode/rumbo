package tool

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // registers the format with image.Decode
	_ "image/jpeg"
	"image/png"
	"math"
	"net/http"

	"github.com/LiasCode/rumbo/protocol"
)

// Limits on an image shown to the model. Models see an image as tokens in
// proportion to its area, and resize anything larger than about this
// themselves, so a larger one costs the same and only travels further.
const (
	// maxImageSide bounds the longer side of an image read from a file.
	maxImageSide = 1568
	// maxImagePixels bounds its area.
	maxImagePixels = 1_150_000
	// maxImageBytes is the largest file sent as it is: model APIs take
	// about 5 MB of base64 per image.
	maxImageBytes = 3_750_000
)

// imageType returns the media type of data when it is an image a model can
// be shown, and "" otherwise.
func imageType(data []byte) string {
	switch kind := http.DetectContentType(data); kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return kind
	}

	return ""
}

// showImage prepares the image in data for the model: as it is when it is
// small enough, and otherwise smaller, as a PNG. It returns the image and
// what to tell of it.
func showImage(data []byte, kind string) (protocol.Image, string, error) {
	// WebP has no decoder in Go's standard library, so it goes as it is.
	if kind == "image/webp" {
		if len(data) > maxImageBytes {
			return protocol.Image{}, "", fmt.Errorf("the image is %d bytes, more than the %d a model is shown; convert it to a smaller PNG or JPEG with bash", len(data), maxImageBytes)
		}

		return protocol.Image{MediaType: kind, Data: data}, "webp", nil
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(data))

	if err != nil {
		return protocol.Image{}, "", fmt.Errorf("the image cannot be decoded: %w", err)
	}

	width, height := fitSize(config.Width, config.Height, maxImageSide, maxImageSide, maxImagePixels)
	format := kind[len("image/"):]

	if width == config.Width && height == config.Height && len(data) <= maxImageBytes && readable(data, kind) {
		return protocol.Image{MediaType: kind, Data: data}, fmt.Sprintf("%s, %d×%d", format, width, height), nil
	}

	decoded, _, err := image.Decode(bytes.NewReader(data))

	if err != nil {
		return protocol.Image{}, "", fmt.Errorf("the image cannot be decoded: %w", err)
	}

	shown, err := encodePNG(toRGBA(scale(decoded, width, height)))

	if err != nil {
		return protocol.Image{}, "", err
	}

	if width == config.Width && height == config.Height {
		return shown, fmt.Sprintf("%s, %d×%d", format, width, height), nil
	}

	return shown, fmt.Sprintf("%s, %d×%d, shown at %d×%d", format, config.Width, config.Height, width, height), nil
}

// readable reports whether every model API reads an image as it is. Some do
// not read a PNG of other than 8-bit color, such as the 16-bit gray one
// ImageMagick writes of a black and white picture: OpenAI's Chat Completions
// takes it for a blank one. Such a PNG is sent as 8-bit color instead.
func readable(data []byte, kind string) bool {
	if kind != "image/png" {
		return true
	}

	// The header's bit depth and color type, after the signature and the
	// header's length, name, width, and height.
	if len(data) < 26 {
		return false
	}

	depth, color := data[24], data[25]

	return depth == 8 && (color == 2 || color == 6)
}

// fitSize returns the largest size with the proportions of width×height
// that fits in maxWidth×maxHeight and holds at most maxPixels, never larger
// than the size it was given.
func fitSize(width, height, maxWidth, maxHeight, maxPixels int) (int, int) {
	ratio := min(1, float64(maxWidth)/float64(width), float64(maxHeight)/float64(height))

	if pixels := float64(width) * float64(height) * ratio * ratio; maxPixels > 0 && pixels > float64(maxPixels) {
		ratio *= math.Sqrt(float64(maxPixels) / pixels)
	}

	if ratio >= 1 {
		return width, height
	}

	return max(1, int(float64(width)*ratio)), max(1, int(float64(height)*ratio))
}

// scale returns source resized to width×height, each pixel the average of
// the pixels it covers, which keeps thin lines and small text legible when
// a screen is made smaller.
func scale(source image.Image, width, height int) image.Image {
	bounds := source.Bounds()

	if bounds.Dx() == width && bounds.Dy() == height {
		return source
	}

	rgba := toRGBA(source)
	sourceWidth, sourceHeight := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	out := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := range height {
		top := y * sourceHeight / height
		bottom := max(top+1, (y+1)*sourceHeight/height)

		for x := range width {
			left := x * sourceWidth / width
			right := max(left+1, (x+1)*sourceWidth/width)

			var sum [4]int

			for row := top; row < bottom; row++ {
				offset := row*rgba.Stride + left*4

				for column := left; column < right; column++ {
					sum[0] += int(rgba.Pix[offset])
					sum[1] += int(rgba.Pix[offset+1])
					sum[2] += int(rgba.Pix[offset+2])
					sum[3] += int(rgba.Pix[offset+3])
					offset += 4
				}
			}

			count := (bottom - top) * (right - left)
			at := y*out.Stride + x*4

			for channel := range 4 {
				out.Pix[at+channel] = uint8(sum[channel] / count)
			}
		}
	}

	return out
}

// toRGBA returns img with 8-bit samples of color, which is how a PNG of it is
// then written.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}

	bounds := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, draw.Src)

	return rgba
}

// encodePNG returns img as a PNG image for the model.
func encodePNG(img image.Image) (protocol.Image, error) {
	var buffer bytes.Buffer

	if err := png.Encode(&buffer, img); err != nil {
		return protocol.Image{}, err
	}

	return protocol.Image{MediaType: "image/png", Data: buffer.Bytes()}, nil
}
