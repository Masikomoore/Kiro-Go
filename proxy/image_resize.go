// Inbound image downscaling (experimental, compat.imageDownscale).
//
// Kiro does not downscale images itself: a phone screenshot sent as a ~700KB
// base64 field alone triggers CONTENT_LENGTH_EXCEEDS_THRESHOLD. Large PNG and
// JPEG images are shrunk to a 1568px long side (the size the vision encoder
// works at anyway) and re-encoded as JPEG under maxDownscaledImageBytes. GIF
// is passed through because it may be animated, and WebP because the standard
// library cannot decode it. Any failure keeps the original image. Limits follow
// hank9999/kiro.rs (MIT), src/image_resize.rs.
package proxy

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"

	"kiro-go/config"
)

const (
	maxImageLongSide        = 1568
	maxDownscaledImageBytes = 400 * 1024
	minImageLongSide        = 256
	maxDecodePixels         = 40_000_000
	startJPEGQuality        = 85
	minJPEGQuality          = 35
)

// maybeDownscaleImage returns a smaller JPEG encoding of a PNG or JPEG image
// that is too large, or ok=false to keep the original.
func maybeDownscaleImage(decoded []byte, format string) (out []byte, outFormat string, ok bool) {
	if !config.GetCompatConfig().ImageDownscale || (format != "png" && format != "jpeg") {
		return nil, "", false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", false
	}
	longSide := maxInt(cfg.Width, cfg.Height)
	if longSide <= maxImageLongSide && len(decoded) <= maxDownscaledImageBytes {
		return nil, "", false
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return nil, "", false
	}

	var src image.Image
	if format == "png" {
		src, err = png.Decode(bytes.NewReader(decoded))
	} else {
		src, err = jpeg.Decode(bytes.NewReader(decoded))
	}
	if err != nil {
		return nil, "", false
	}
	flat := flattenOnWhite(src)

	target := minInt(longSide, maxImageLongSide)
	for target >= minImageLongSide {
		w, h := fitLongSide(cfg.Width, cfg.Height, target)
		scaled := flat
		if w != cfg.Width || h != cfg.Height {
			scaled = boxDownscale(flat, w, h)
		}
		for q := startJPEGQuality; q >= minJPEGQuality; q -= 10 {
			var buf bytes.Buffer
			if jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: q}) != nil {
				return nil, "", false
			}
			if buf.Len() <= maxDownscaledImageBytes {
				if buf.Len() >= len(decoded) {
					return nil, "", false
				}
				return buf.Bytes(), "jpeg", true
			}
		}
		target = target * 3 / 4
	}
	return nil, "", false
}

// downscaledImageBase64 is maybeDownscaleImage with base64 output.
func downscaledImageBase64(decoded []byte, format string) (string, string, bool) {
	out, outFormat, ok := maybeDownscaleImage(decoded, format)
	if !ok {
		return "", "", false
	}
	return base64.StdEncoding.EncodeToString(out), outFormat, true
}

func fitLongSide(w, h, longSide int) (int, int) {
	if w >= h {
		if w <= longSide {
			return w, h
		}
		return longSide, maxInt(1, h*longSide/w)
	}
	if h <= longSide {
		return w, h
	}
	return maxInt(1, w*longSide/h), longSide
}

// flattenOnWhite composites the image onto white, since JPEG has no alpha.
func flattenOnWhite(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

// boxDownscale averages each destination pixel's source rectangle, which is
// a good-quality filter for shrinking.
func boxDownscale(src *image.RGBA, dw, dh int) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy0, sy1 := y*sh/dh, (y+1)*sh/dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < dw; x++ {
			sx0, sx1 := x*sw/dw, (x+1)*sw/dw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, b, a, n uint32
			for sy := sy0; sy < sy1; sy++ {
				off := src.PixOffset(sx0, sy)
				for sx := sx0; sx < sx1; sx++ {
					r += uint32(src.Pix[off])
					g += uint32(src.Pix[off+1])
					b += uint32(src.Pix[off+2])
					a += uint32(src.Pix[off+3])
					off += 4
					n++
				}
			}
			d := dst.PixOffset(x, y)
			dst.Pix[d] = uint8(r / n)
			dst.Pix[d+1] = uint8(g / n)
			dst.Pix[d+2] = uint8(b / n)
			dst.Pix[d+3] = uint8(a / n)
		}
	}
	return dst
}
