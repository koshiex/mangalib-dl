package downloader

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif" // register GIF decoder
	"image/png"
	"net/http"

	_ "golang.org/x/image/bmp"  // register BMP decoder
	_ "golang.org/x/image/webp" // register WebP decoder
)

// normalizeImage keeps JPEG and PNG as is and converts other formats to PNG.
func normalizeImage(data []byte) (ext string, out []byte, err error) {
	switch ct := http.DetectContentType(data); ct {
	case "image/jpeg":
		return "jpg", data, nil
	case "image/png":
		return "png", data, nil
	case "image/webp", "image/gif", "image/bmp":
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return "", nil, fmt.Errorf("decode %s: %w", ct, err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return "", nil, fmt.Errorf("encode png: %w", err)
		}
		return "png", buf.Bytes(), nil
	default:
		return "", nil, fmt.Errorf("unsupported image type %q", ct)
	}
}
