package computer

import (
	"bytes"
	"errors"
	"image"
	"strconv"
)

// decodePPM reads a binary PPM (P6) of 8-bit samples, as grim writes it,
// which is quicker for it to write than a PNG.
func decodePPM(data []byte) (*image.RGBA, error) {
	fields := make([]int, 0, 3)
	rest := data

	if !bytes.HasPrefix(rest, []byte("P6")) {
		return nil, errors.New("not a binary PPM image")
	}

	rest = rest[2:]

	// The width, the height, and the largest sample, separated by white
	// space and comments, and then one white space before the pixels.
	for len(fields) < 3 {
		rest = skipSpace(rest)
		end := 0

		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}

		if end == 0 {
			return nil, errors.New("malformed PPM header")
		}

		value, err := strconv.Atoi(string(rest[:end]))

		if err != nil {
			return nil, err
		}

		fields = append(fields, value)
		rest = rest[end:]
	}

	width, height, largest := fields[0], fields[1], fields[2]

	if largest != 255 {
		return nil, errors.New("only PPM images of 8-bit samples are read")
	}

	if len(rest) == 0 {
		return nil, errors.New("PPM image without pixels")
	}

	rest = rest[1:]

	if width <= 0 || height <= 0 || len(rest) < width*height*3 {
		return nil, errors.New("PPM image shorter than its size")
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	for index := range width * height {
		img.Pix[index*4] = rest[index*3]
		img.Pix[index*4+1] = rest[index*3+1]
		img.Pix[index*4+2] = rest[index*3+2]
		img.Pix[index*4+3] = 255
	}

	return img, nil
}

func skipSpace(data []byte) []byte {
	for len(data) > 0 {
		switch {
		case data[0] == '#':
			end := bytes.IndexByte(data, '\n')

			if end < 0 {
				return nil
			}

			data = data[end+1:]

		case data[0] == ' ' || data[0] == '\t' || data[0] == '\n' || data[0] == '\r':
			data = data[1:]

		default:
			return data
		}
	}

	return data
}
