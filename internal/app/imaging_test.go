package app

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestTallThumbnailFitsBothAxesWithoutChangingOriginal(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 4096))
	for y := range 4096 {
		for x := range 64 {
			img.SetRGBA(x, y, color.RGBA{200, uint8(y), 20, 255})
		}
	}
	var source bytes.Buffer
	if e := png.Encode(&source, img); e != nil {
		t.Fatal(e)
	}
	before := append([]byte(nil), source.Bytes()...)
	out, e := processImage(context.Background(), PageValue{Bytes: source.Bytes(), MIME: "image/png"}, Transform{Width: 320, Quality: 80, Format: "png", Thumbnail: true}, 1_000_000, 1<<20, false)
	if e != nil {
		t.Fatal(e)
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(out.Bytes))
	if e != nil || cfg.Width > 320 || cfg.Height > 480 || cfg.Height != 480 {
		t.Fatalf("unbounded or cropped thumbnail: %+v %v", cfg, e)
	}
	if !bytes.Equal(before, source.Bytes()) {
		t.Fatal("thumbnail transformation mutated original bytes")
	}
}
func TestImageWorkerRejectsNonImageAndPixelBomb(t *testing.T) {
	for _, source := range []PageValue{{Bytes: []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><image href="file:///etc/passwd"/></svg>`), MIME: "image/png"}, {Bytes: testPNG(t), MIME: "image/png"}} {
		maxPixels := int64(1)
		if _, e := processImage(context.Background(), source, Transform{Width: 320, Quality: 80, Format: "png"}, maxPixels, 1<<20, false); e == nil {
			t.Fatal("unsupported source or oversized dimensions entered image decoder")
		}
	}
}
