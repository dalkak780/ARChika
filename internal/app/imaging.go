package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strings"

	_ "image/gif"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

type Transform struct {
	Width     int
	Height    int
	Quality   int
	Format    string
	Thumbnail bool
}
type ImageProcessor interface {
	Version() string
	Process(context.Context, PageValue, Transform, int64) (PageValue, error)
}
type GoProcessor struct{}

func (GoProcessor) Version() string { return "go-imaging-2" }

var errUnsupportedTransform = errors.New("unsupported image transformation")

func (GoProcessor) Process(ctx context.Context, input PageValue, t Transform, maxPixels int64) (PageValue, error) {
	if e := ctx.Err(); e != nil {
		return PageValue{}, e
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(input.Bytes))
	if e != nil {
		return PageValue{}, errUnsupportedTransform
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width) > maxPixels/int64(cfg.Height) {
		return PageValue{}, errors.New("image exceeds pixel budget")
	}
	img, e := imaging.Decode(bytes.NewReader(input.Bytes), imaging.AutoOrientation(true))
	if e != nil {
		return PageValue{}, errUnsupportedTransform
	}
	if e = ctx.Err(); e != nil {
		return PageValue{}, e
	}
	if t.Thumbnail {
		img = imaging.Fit(img, t.Width, t.thumbnailHeight(), imaging.Lanczos)
	} else if t.Width > 0 && img.Bounds().Dx() > t.Width {
		img = imaging.Resize(img, t.Width, 0, imaging.Lanczos)
	}
	var out bytes.Buffer
	format := t.Format
	if format == "" {
		format = "png"
	}
	mime := "image/png"
	switch format {
	case "jpeg":
		mime = "image/jpeg"
		e = jpeg.Encode(&out, img, &jpeg.Options{Quality: t.Quality})
	case "png":
		e = png.Encode(&out, img)
	default:
		return PageValue{}, errUnsupportedTransform
	}
	if e != nil {
		return PageValue{}, e
	}
	return PageValue{Bytes: out.Bytes(), MIME: mime, Still: animated(input)}, ctx.Err()
}
func animated(v PageValue) bool {
	switch v.MIME {
	case "image/gif":
		return true
	case "image/png":
		return bytes.Contains(v.Bytes, []byte("acTL"))
	case "image/webp":
		return len(v.Bytes) > 16 && bytes.Contains(v.Bytes[:min(len(v.Bytes), 256)], []byte("ANIM"))
	case "image/avif":
		return bytes.Contains(v.Bytes[:min(len(v.Bytes), 128)], []byte("avis"))
	case "image/jxl":
		// ponytail: decoder-specific frame counts; explicitly label JXL compatibility output as still-frame.
		return true
	}
	return false
}
func sniffMIME(b []byte, fallback string) string {
	if len(b) > 12 {
		switch {
		case bytes.Equal(b[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}):
			return "image/png"
		case string(b[:3]) == "GIF":
			return "image/gif"
		case b[0] == 255 && b[1] == 216:
			return "image/jpeg"
		case string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
			return "image/webp"
		case string(b[4:8]) == "ftyp" && (bytes.Contains(b[:min(64, len(b))], []byte("avif")) || bytes.Contains(b[:min(64, len(b))], []byte("avis"))):
			return "image/avif"
		case b[0] == 255 && b[1] == 10 || bytes.Equal(b[:12], []byte{0, 0, 0, 12, 'J', 'X', 'L', ' ', 13, 10, 135, 10}):
			return "image/jxl"
		}
	}
	return fallback
}
func boundedRead(ctx context.Context, r io.Reader, limit int64) ([]byte, error) {
	var b bytes.Buffer
	buf := make([]byte, 32<<10)
	for {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		n, e := r.Read(buf)
		if n > 0 {
			if int64(b.Len())+int64(n) > limit {
				return nil, errors.New("page exceeds configured byte limit")
			}
			_, _ = b.Write(buf[:n])
		}
		if e == io.EOF {
			return b.Bytes(), nil
		}
		if e != nil {
			return nil, e
		}
		if n == 0 {
			return nil, io.ErrNoProgress
		}
	}
}
func validateTransform(format string, width, quality int) (Transform, error) {
	if format == "jpg" {
		format = "jpeg"
	}
	if format != "" && format != "jpeg" && format != "png" && format != "webp" {
		return Transform{}, errors.New("format must be jpeg, png or webp")
	}
	if width < 0 || width > 4096 || quality < 1 || quality > 100 {
		return Transform{}, errors.New("invalid transformation dimensions or quality")
	}
	if width > 0 && format == "" {
		format = "png"
	}
	return Transform{Width: width, Quality: quality, Format: strings.ToLower(format)}, nil
}

func (t Transform) thumbnailHeight() int {
	if t.Height > 0 {
		return t.Height
	}
	return 480
}
