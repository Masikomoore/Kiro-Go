package proxy

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"kiro-go/config"
	"testing"
)

func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8((x*7 + y*13 + x*y) % 256)
			img.Set(x, y, color.NRGBA{R: v, G: 255 - v, B: uint8(x % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func enableImageDownscale(t *testing.T) {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.UpdateCompatSettings(config.CompatSettings{ImageDownscale: true}); err != nil {
		t.Fatalf("update compat: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })
}

func TestParseBase64ImageDownscalesLargeScreenshot(t *testing.T) {
	enableImageDownscale(t)
	raw := noisyPNG(t, 1206, 2622)
	if len(raw) <= maxDownscaledImageBytes {
		t.Fatalf("fixture too small: %d bytes", len(raw))
	}

	img := parseBase64Image(base64.StdEncoding.EncodeToString(raw), "png")
	if img == nil || img.Format != "jpeg" {
		t.Fatalf("expected a JPEG re-encode, got %+v", img)
	}
	out, _ := base64.StdEncoding.DecodeString(img.Source.Bytes)
	if len(out) > maxDownscaledImageBytes {
		t.Fatalf("downscaled image is %d bytes", len(out))
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if cfg.Height > maxImageLongSide || cfg.Width > maxImageLongSide {
		t.Fatalf("long side must be at most %d, got %dx%d", maxImageLongSide, cfg.Width, cfg.Height)
	}
}

func TestParseBase64ImageKeepsSmallImageAndSwitchOff(t *testing.T) {
	enableImageDownscale(t)
	small := base64.StdEncoding.EncodeToString(noisyPNG(t, 64, 64))
	if img := parseBase64Image(small, "png"); img == nil || img.Format != "png" || img.Source.Bytes != small {
		t.Fatal("small images must pass through unchanged")
	}

	_ = config.UpdateCompatSettings(config.CompatSettings{})
	big := base64.StdEncoding.EncodeToString(noisyPNG(t, 2000, 2000))
	if img := parseBase64Image(big, "png"); img == nil || img.Source.Bytes != big {
		t.Fatal("switch off must leave images alone")
	}
}

func TestBoxDownscaleAverages(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.RGBA{0, 0, 0, 255})
	src.Set(1, 0, color.RGBA{255, 255, 255, 255})
	src.Set(0, 1, color.RGBA{0, 0, 0, 255})
	src.Set(1, 1, color.RGBA{255, 255, 255, 255})
	got := boxDownscale(src, 1, 1).RGBAAt(0, 0)
	if got.R < 126 || got.R > 128 || got.A != 255 {
		t.Fatalf("expected mid gray, got %+v", got)
	}
}
