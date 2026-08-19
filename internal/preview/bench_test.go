package preview

import (
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkSixelEncodeCold measures the first encode, which pays per-image
// median-cut palette quantization. It is the baseline the shared warm palette
// eliminates on subsequent encodes.
func BenchmarkSixelEncodeCold(b *testing.B) {
	img := makeTestImage(1280, 720)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m2 := &Manager{sixelDither: false} // fresh encoder each time = cold palette
		if _, err := m2.sixelEncode(img, 380, 500); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSixelEncodeWarm measures encodes that reuse the shared warm palette
// and go-sixel's cached lookup table — the steady-state path.
func BenchmarkSixelEncodeWarm(b *testing.B) {
	m := &Manager{sixelDither: false}
	img := makeTestImage(1280, 720)
	if _, err := m.sixelEncode(img, 380, 500); err != nil { // seed palette
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.sixelEncode(makeTestImage(1280, 720), 380, 500); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeResizeJPEG measures the full decode + BiLinear downscale of a
// 1280x720 JPEG to a ~380x500 preview, isolating the non-encode half.
func BenchmarkDecodeResizeJPEG(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "thumb.jpg")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	if err := jpeg.Encode(f, makeTestImage(1280, 720), &jpeg.Options{Quality: 85}); err != nil {
		b.Fatal(err)
	}
	f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		img, err := decodeImage(b.Context(), path)
		if err != nil {
			b.Fatal(err)
		}
		img = fitImage(img, 380, 500)
		if img.Bounds().Dx() == 0 {
			b.Fatal("empty image")
		}
	}
}
