package preview

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// testWebP is a real 64x40 WebP file (RIFF....WEBP). YouTube serves its
// thumbnails as WebP even though gotube caches them with ".jpg" names, so
// decodeImage must sniff the magic and decode WebP explicitly rather than
// relying on image.Decode's registered jpeg/png/gif decoders.
const testWebP = "UklGRpIAAABXRUJQVlA4IIYAAACQBQCdASpAACgAPpFAnUolo6KhpXgLMLASCWYAxfgzeV7ygZB//94gAWCy5uol7qIfK+gMgAD+8ALNp9XZ9yvD3ZwX/+0iD+pEH8DMYIhNBJLFKGgeFiWF8ZFub0j+WZit7Ipqm937TRou6ctPcUqpzqN2KQJ8yRowlKbBIsbnodv/yR4AAA=="

// TestDecodeImageWebP verifies a WebP file (even under a .jpg name) decodes
// to the expected dimensions, which is what real YouTube thumbnails require.
func TestDecodeImageWebP(t *testing.T) {
	f := filepath.Join(t.TempDir(), "thumb.jpg") // deliberately a .jpg name
	data, err := base64.StdEncoding.DecodeString(testWebP)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if err := os.WriteFile(f, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	img, err := decodeImage(t.Context(), f)
	if err != nil {
		t.Fatalf("decodeImage: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 64 || b.Dy() != 40 {
		t.Fatalf("decoded size = %dx%d, want 64x40", b.Dx(), b.Dy())
	}
}

// TestIsWebP verifies the RIFF/WEBP magic sniffing used by decodeImage.
func TestIsWebP(t *testing.T) {
	webp := []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'}
	if !isWebP(webp) {
		t.Fatalf("isWebP returned false for a valid WebP header")
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10}
	if isWebP(jpeg) {
		t.Fatalf("isWebP returned true for a JPEG header")
	}
	if isWebP(nil) || isWebP([]byte{'R', 'I', 'F', 'F'}) {
		t.Fatalf("isWebP accepted a truncated header")
	}
}
