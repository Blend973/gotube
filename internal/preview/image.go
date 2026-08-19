package preview

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"  // registered image decoders for the thumbnail cache
	_ "image/jpeg" // registered image decoders for the thumbnail cache
	_ "image/png"  // registered image decoders for the thumbnail cache
	"io"
	"os"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// decodeImage opens and decodes an image file. The context is advisory: it is
// checked right before the blocking decode so a stale/canceled request bails
// out instead of spending CPU on an image nobody wants anymore. The decode
// itself is CPU-bound and cannot be preempted by context cancellation, which
// is why the generation check after encoding is the real correctness guard
// (see sixelPipeline).
//
// YouTube serves its thumbnails as WebP even though the cached files are
// named ".jpg". x/image/webp does not self-register with image.Decode, so we
// sniff the RIFF/WEBP magic and decode WebP explicitly, falling back to the
// standard image decoders for anything else.
func decodeImage(ctx context.Context, path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Peek at the header to detect WebP (RIFF....WEBP). We consume up to 12
	// bytes; for non-WebP files we seek back so image.Decode sees the whole
	// stream.
	var header [12]byte
	n, _ := io.ReadFull(f, header[:])
	if isWebP(header[:n]) {
		// Re-join the header with the rest of the stream for webp.Decode.
		return webp.Decode(io.MultiReader(bytes.NewReader(header[:n]), f))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	return img, err
}

// isWebP reports whether header (a prefix of the file) matches the WebP
// container magic: "RIFF" at offset 0 and "WEBP" at offset 8.
func isWebP(header []byte) bool {
	return len(header) >= 12 &&
		header[0] == 'R' && header[1] == 'I' && header[2] == 'F' && header[3] == 'F' &&
		header[8] == 'W' && header[9] == 'E' && header[10] == 'B' && header[11] == 'P'
}

// fitImage scales src to the largest size that fits entirely within
// maxW x maxH, preserving aspect ratio. It is the Go analogue of Yazi's
// Image::downscale(): shrink before encoding so the Sixel quantizer, the
// payload size, and the resulting TTY write all stay small.
//
// If src already fits within the target it is returned unchanged (no copy).
// Both maxW and maxH are treated as strict upper bounds; a zero bound means
// "no limit" on that axis.
func fitImage(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= 0 || srcH <= 0 {
		return src
	}
	if maxW <= 0 {
		maxW = srcW
	}
	if maxH <= 0 {
		maxH = srcH
	}
	if srcW <= maxW && srcH <= maxH {
		return src
	}

	// Pick the smaller scale so neither dimension exceeds its bound.
	scale := float64(maxW) / float64(srcW)
	if hScale := float64(maxH) / float64(srcH); hScale < scale {
		scale = hScale
	}
	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	// Bilinear is the fast path for preview downscales. Catmull-Rom is
	// sharper but several times slower, and at preview sizes the extra
	// sharpness is imperceptible — decode + encode dominate the render.
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}
