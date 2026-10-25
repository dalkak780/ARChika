//go:build vips

package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/davidbyttow/govips/v2/vips"
)

type VipsProcessor struct{}

func newImageProcessor() ImageProcessor { return VipsProcessor{} }
func (VipsProcessor) Version() string {
	return fmt.Sprintf("govips-2.20-libvips-%d.%d.%d-2", vips.MajorVersion, vips.MinorVersion, vips.MicroVersion)
}
func (VipsProcessor) Process(ctx context.Context, input PageValue, t Transform, maxPixels int64) (PageValue, error) {
	if e := ctx.Err(); e != nil {
		return PageValue{}, e
	}
	if e := vips.Startup(&vips.Config{ConcurrencyLevel: 1, MaxCacheMem: 16 << 20, MaxCacheFiles: 0, MaxCacheSize: 0}); e != nil {
		return PageValue{}, e
	}
	params := vips.NewImportParams()
	params.NumPages.Set(1)
	img, e := vips.LoadImageFromBuffer(input.Bytes, params)
	if e != nil {
		return PageValue{}, errUnsupportedTransform
	}
	defer img.Close()
	if img.Width() < 1 || img.Height() < 1 || int64(img.Width()) > maxPixels/int64(img.Height()) {
		return PageValue{}, errors.New("image exceeds pixel budget")
	}
	if e = img.AutoRotate(); e != nil {
		return PageValue{}, e
	}
	scale := 1.0
	if t.Width > 0 {
		scale = min(scale, float64(t.Width)/float64(img.Width()))
	}
	if t.Thumbnail {
		scale = min(scale, float64(t.thumbnailHeight())/float64(img.Height()))
	}
	if scale < 1 {
		e = img.Resize(scale, vips.KernelLanczos3)
		if e != nil {
			return PageValue{}, e
		}
	}
	format := t.Format
	if format == "" {
		format = "png"
	}
	out := PageValue{Still: animated(input)}
	switch format {
	case "jpeg":
		out.MIME = "image/jpeg"
		p := vips.NewJpegExportParams()
		p.Quality = t.Quality
		p.StripMetadata = false
		out.Bytes, _, e = img.ExportJpeg(p)
	case "png":
		out.MIME = "image/png"
		p := vips.NewPngExportParams()
		p.Compression = 6
		out.Bytes, _, e = img.ExportPng(p)
	case "webp":
		out.MIME = "image/webp"
		p := vips.NewWebpExportParams()
		p.Quality = t.Quality
		out.Bytes, _, e = img.ExportWebp(p)
	default:
		return PageValue{}, errUnsupportedTransform
	}
	if e != nil {
		return PageValue{}, e
	}
	return out, ctx.Err()
}
