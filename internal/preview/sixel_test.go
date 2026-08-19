package preview

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/user/gotube/internal/config"
)

// makeTestImage returns a solid-color RGBA image of the given size, useful
// for exercising decode/resize/encode without external fixtures.
func makeTestImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 3), uint8(y * 5), 120, 255})
		}
	}
	return img
}

// TestFitImageDownscales verifies aspect-preserving downscale: an image larger
// than the target on both axes is shrunk so neither dimension exceeds the
// bound while the ratio is preserved.
func TestFitImageDownscales(t *testing.T) {
	src := makeTestImage(320, 200)
	out := fitImage(src, 100, 100)
	b := out.Bounds()
	if b.Dx() > 100 || b.Dy() > 100 {
		t.Fatalf("fitImage = %dx%d, want within 100x100", b.Dx(), b.Dy())
	}
	// 320x200 scaled to fit 100x100 → width binds (1.6 aspect vs 1.0 box) →
	// 100 wide, 62.5→62 high.
	if b.Dx() != 100 || b.Dy() != 62 {
		t.Fatalf("fitImage = %dx%d, want 100x62", b.Dx(), b.Dy())
	}
}

// TestFitImageReturnsOriginal verifies a source already within bounds is not
// copied or resized.
func TestFitImageReturnsOriginal(t *testing.T) {
	src := makeTestImage(50, 30)
	out := fitImage(src, 100, 100)
	if out != image.Image(src) {
		t.Fatalf("fitImage returned a new image for an in-bounds source")
	}
}

// TestEncodeSixelFraming verifies the production encoder produces a framed
// DECSIXEL sequence (ESC P ... ESC \) in memory, not written to the terminal.
func TestEncodeSixelFraming(t *testing.T) {
	m := &Manager{sixelDither: false}
	payload, err := m.sixelEncode(makeTestImage(320, 200), 100, 100)
	if err != nil {
		t.Fatalf("sixelEncode: %v", err)
	}
	if len(payload) < 4 {
		t.Fatalf("payload too short: %q", payload)
	}
	// DECSIXEL starts with ESC P and is terminated by ESC \.
	if payload[0] != 0x1b || payload[1] != 'P' {
		t.Fatalf("payload does not start with ESC P: %q", payload[:4])
	}
	if payload[len(payload)-2] != 0x1b || payload[len(payload)-1] != '\\' {
		t.Fatalf("payload does not end with ESC \\: %q", payload[len(payload)-2:])
	}
}

// TestFlushSixelWrites verifies a current-generation burst is written as a
// single ordered sequence (cursor prefix then payload).
func TestFlushSixelWrites(t *testing.T) {
	f := &fakeTty{}
	m := &Manager{tty: f, sequence: 7}
	m.FlushSixel(&SixelBurst{
		Gen:          7,
		CursorPrefix: []byte("\x1b[1;1H"),
		Payload:      []byte("\x1bP0;q...\x1b\\"),
	})
	want := "\x1b[1;1H\x1bP0;q...\x1b\\"
	if f.buf.String() != want {
		t.Fatalf("tty write = %q, want %q", f.buf.String(), want)
	}
}

// TestFlushSixelDropsStale verifies bursts from an older generation (region
// cleared or selection moved while encoding) are discarded, as are writes
// after Close().
func TestFlushSixelDropsStale(t *testing.T) {
	f := &fakeTty{}
	m := &Manager{tty: f, sequence: 9}
	m.FlushSixel(&SixelBurst{Gen: 8, Payload: []byte("stale")})
	if f.buf.Len() != 0 {
		t.Fatalf("stale burst was written: %q", f.buf.String())
	}

	f2 := &fakeTty{}
	m2 := &Manager{tty: f2, sequence: 9, closed: true}
	m2.FlushSixel(&SixelBurst{Gen: 9, Payload: []byte("late")})
	if f2.buf.Len() != 0 {
		t.Fatalf("burst written after close: %q", f2.buf.String())
	}
}

// TestGotoPrefix verifies cursor positioning uses absolute screen coordinates
// so the image lands in the right cell rectangle.
func TestGotoPrefix(t *testing.T) {
	ti, err := tcell.LookupTerminfo("xterm-256color")
	if err != nil {
		t.Skipf("no xterm-256color terminfo: %v", err)
	}
	m := &Manager{ti: ti}
	got := string(m.gotoPrefix(Rect{X: 2, Y: 3}))
	if got != "\x1b[4;3H" {
		t.Fatalf("gotoPrefix = %q, want %q", got, "\x1b[4;3H")
	}
}

// TestHasSixelSupport serves as a regression guard for the TERM-based
// detection: known sixel-capable terminals must be recognized.
func TestHasSixelSupport(t *testing.T) {
	cases := []struct {
		term string
		want bool
	}{
		{"foot", true},
		{"xterm-256color", false}, // plain xterm is not necessarily sixel-enabled
		{"", false},
	}
	for _, tc := range cases {
		t.Setenv("TERM", tc.term)
		if got := hasSixelSupport(); got != tc.want {
			t.Errorf("TERM=%q hasSixelSupport = %v, want %v", tc.term, got, tc.want)
		}
	}
}

// TestResolveCellPixels verifies the cell-pixel resolution priority used to
// size sixel images: explicit config > detected terminal value > 8x16 default.
func TestResolveCellPixels(t *testing.T) {
	// Nothing known → conservative default.
	cw, ch := resolveCellPixels(nil, 0, 0)
	if cw != 8 || ch != 16 {
		t.Fatalf("default cell pixels = %dx%d, want 8x16", cw, ch)
	}

	// Detected terminal value fills in when config is unset.
	cw, ch = resolveCellPixels(nil, 10, 21)
	if cw != 10 || ch != 21 {
		t.Fatalf("detected cell pixels = %dx%d, want 10x21", cw, ch)
	}

	// Explicit config overrides detection.
	cfg := &config.PreviewConfig{CellWidth: 7, CellHeight: 15}
	cw, ch = resolveCellPixels(cfg, 10, 21)
	if cw != 7 || ch != 15 {
		t.Fatalf("configured cell pixels = %dx%d, want 7x15 (config wins)", cw, ch)
	}

	// Per-axis: a config value only overrides that axis; the other uses
	// detection.
	cfgPartial := &config.PreviewConfig{CellWidth: 9}
	cw, ch = resolveCellPixels(cfgPartial, 10, 21)
	if cw != 9 || ch != 21 {
		t.Fatalf("partial config cell pixels = %dx%d, want 9x21", cw, ch)
	}
}

// TestParseCellSizeReport verifies parsing the answer to "CSI 16 t"
// (ESC [ 6 ; cellheight ; cellwidth t).
func TestParseCellSizeReport(t *testing.T) {
	w, h, ok := parseCellSizeReport([]byte("\x1b[6;21;10t"))
	if !ok || w != 10 || h != 21 {
		t.Fatalf("parse = %dx%d ok=%v, want 10x21 ok=true", w, h, ok)
	}

	// Garbage / truncated / wrong report id must be rejected.
	if _, _, ok := parseCellSizeReport(nil); ok {
		t.Fatalf("nil buffer parsed OK")
	}
	if _, _, ok := parseCellSizeReport([]byte("hello")); ok {
		t.Fatalf("non-escape buffer parsed OK")
	}
	if _, _, ok := parseCellSizeReport([]byte("\x1b[5;21;10t")); ok {
		t.Fatalf("wrong report id parsed OK (should be 6)")
	}
	if _, _, ok := parseCellSizeReport([]byte("\x1b[6;21;10")); ok {
		t.Fatalf("missing trailing t parsed OK")
	}
}

// TestQueryCellSizeReport drives the full query round-trip against in-memory
// pipes: a fake terminal reads the "CSI 16 t" request and answers with a cell
// size report, which queryCellSizeReport should parse into cell dimensions.
func TestQueryCellSizeReport(t *testing.T) {
	toTermR, toTermW := io.Pipe()     // app writes query → fake terminal reads
	fromTermR, fromTermW := io.Pipe() // fake terminal writes report → app reads

	go func() {
		defer fromTermW.Close()
		buf := make([]byte, 8)
		if _, err := toTermR.Read(buf); err != nil {
			return
		}
		if _, err := fromTermW.Write([]byte("\x1b[6;21;10t")); err != nil {
			return
		}
	}()

	w, h := queryCellSizeReport(fromTermR, toTermW, 2*time.Second)
	if w != 10 || h != 21 {
		t.Fatalf("queryCellSizeReport = %dx%d, want 10x21", w, h)
	}
}

// TestQueryCellSizeReportTimeout verifies a terminal that never answers causes
// a timed-out (0,0) result rather than a hang.
func TestQueryCellSizeReportTimeout(t *testing.T) {
	r, pw := io.Pipe() // reader that never receives
	var w bytes.Buffer // writes never block
	cw, ch := queryCellSizeReport(r, &w, 50*time.Millisecond)
	pw.Close() // unblock the internal reader goroutine
	if cw != 0 || ch != 0 {
		t.Fatalf("timeout should yield 0x0, got %dx%d", cw, ch)
	}
}

// TestUsesSixel verifies the TUI only spends a cell-size query round-trip when
// the sixel renderer is actually selected.
func TestUsesSixel(t *testing.T) {
	t.Setenv("IMAGE_RENDERER", "sixel")
	if !UsesSixel(nil) {
		t.Fatalf("IMAGE_RENDERER=sixel should select the sixel renderer")
	}

	t.Setenv("IMAGE_RENDERER", "kitty")
	t.Setenv("KITTY_WINDOW_ID", "1")
	if UsesSixel(nil) {
		t.Fatalf("cat detected kitty should not select sixel")
	}
}

// TestRenderSixelPositionsAtImageRect is a regression test for the camera-card
// positioning bug: renderCached (correctly) applies imageRect once, so the
// burst's cursor prefix must land at that same inset rect. Re-applying
// imageRect inside the async path double-insets X and bleeds the image over
// the pane's right border onto the results list. This captures the actual
// burst posted to the event loop and asserts kitty/ueberzugpp-equivalent
// placement.
func TestRenderSixelPositionsAtImageRect(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("simulation screen init: %v", err)
	}
	defer screen.Fini()

	ti, err := tcell.LookupTerminfo("xterm-256color")
	if err != nil {
		t.Skipf("no xterm-256color terminfo: %v", err)
	}

	// Write a small thumbnail (as renderCached would have cached it).
	f := filepath.Join(t.TempDir(), "thumb.png")
	fh, err := os.Create(f)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(fh, makeTestImage(320, 200)); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	fh.Close()

	m := &Manager{
		screen:   screen,
		ti:       ti,
		sequence: 5,
		itemKey:  "video-id",
		itemPath: f,
		region:   Rect{X: 0, Y: 2, W: 40, H: 25},
		cellW:    10,
		cellH:    20,
		inflight: map[string]struct{}{},
	}

	// renderCached passes the imageRect'd rect: the single inset.
	imgRect := m.imageRect(Rect{X: 0, Y: 2, W: 40, H: 25})
	m.renderSixel(f, imgRect)

	burst := drainSixelBurst(t, screen)
	if burst == nil {
		return
	}

	// Single inset: rect is Rect{1,2,38,25} → TGoto(1,2) = "\x1b[3;2H".
	// The double-inset bug produced "\x1b[3;3H" instead.
	if got := string(burst.CursorPrefix); got != "\x1b[3;2H" {
		t.Fatalf("sixel cursor prefix = %q, want %q (single inset)", got, "\x1b[3;2H")
	}
}

// drainSixelBurst polls the screen event queue until the sideel burst from a
// background render arrives, failing the test on timeout.
func drainSixelBurst(t *testing.T, screen tcell.Screen) *SixelBurst {
	t.Helper()
	ch := make(chan *SixelBurst, 1)
	go func() {
		for {
			ev := screen.PollEvent()
			if ev == nil {
				return
			}
			if ie, ok := ev.(*tcell.EventInterrupt); ok {
				if b, ok := ie.Data().(*SixelBurst); ok {
					ch <- b
					return
				}
			}
		}
	}()
	select {
	case b := <-ch:
		return b
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for sixel burst")
		return nil
	}
}

// writeTestPNG writes a solid-color PNG to path for exercising encode paths.
func writeTestPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, makeTestImage(w, h)); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

// TestPendingRenderRecoversFastSelection reproduces the race where a render
// is coalesced away by the in-flight gate during fast selection. The stale
// in-flight render later fires a refresh that re-runs Update for the now
// unchanged current item; without the pendingRender flag that recovery would
// be swallowed by Update's unchanged-item dedup and the image would never
// paint until the user navigated (issue: images missing on fast selection).
func TestPendingRenderRecoversFastSelection(t *testing.T) {
	scr := tcell.NewSimulationScreen("")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer scr.Fini()

	tty := &fakeTty{}
	rect := Rect{X: 0, Y: 2, W: 40, H: 25}
	m := &Manager{
		screen:   scr,
		tty:      tty,
		renderer: RendererSixel,
		cellW:    10,
		cellH:    20,
		cacheDir: t.TempDir(),
		inflight: map[string]struct{}{},
		sequence: 1,
		region:   rect,
		itemKey:  "A",
	}
	// B's thumbnail already cached (the fast-selection path is all-cached).
	pathB := m.cachePath("http://example.com/b.png")
	writeTestPNG(t, pathB, 200, 120)
	m.itemPath = pathB

	// Simulate item A's encoder being in flight.
	m.mu.Lock()
	m.sixelDecoding = true
	m.mu.Unlock()

	// Fast-select B while A's render runs: B is cached, its render is
	// attempted but coalesced away, and pendingRender must be recorded.
	m.Update(Item{Key: "B", ThumbnailURL: "http://example.com/b.png"}, rect)

	m.mu.Lock()
	if !m.pendingRender {
		m.mu.Unlock()
		t.Fatalf("pendingRender not set after a coalesced render")
	}
	item := m.itemKey
	m.mu.Unlock()
	if item != "B" {
		t.Fatalf("itemKey = %q, want B", item)
	}

	// A's stale render finishes and frees the gate. The refresh it kicked
	// re-runs Update for the (unchanged) current item; forceRender must make
	// it re-render instead of dedup-swallowing the recovery.
	m.mu.Lock()
	m.sixelDecoding = false
	m.mu.Unlock()

	m.Update(Item{Key: "B", ThumbnailURL: "http://example.com/b.png"}, rect)

	m.mu.Lock()
	if m.pendingRender {
		m.mu.Unlock()
		t.Fatalf("pendingRender not consumed by the force-render update")
	}
	m.mu.Unlock()

	// The forced render must reach the screen as a current burst and flush.
	burst := drainSixelBurst(t, scr)
	if burst == nil {
		return
	}
	m.FlushSixel(burst)
	if tty.buf.Len() == 0 {
		t.Fatalf("recovered render never wrote to the TTY")
	}
}

// TestUpdateDedupsUnchangedCachedItem guards the dedup that the recovery path
// must bypass: a plain, unchanged, already-cached item does NOT re-render.
func TestUpdateDedupsUnchangedCachedItem(t *testing.T) {
	tty := &fakeTty{}
	rect := Rect{X: 0, Y: 2, W: 40, H: 25}
	m := &Manager{
		tty:      tty,
		renderer: RendererSixel,
		cacheDir: t.TempDir(),
		inflight: map[string]struct{}{},
		sequence: 1,
		region:   rect,
		itemKey:  "A",
	}
	pathA := m.cachePath("http://example.com/a.png")
	writeTestPNG(t, pathA, 200, 120)
	m.itemPath = pathA

	m.Update(Item{Key: "A", ThumbnailURL: "http://example.com/a.png"}, rect)

	m.mu.Lock()
	unchangedSeq := m.sequence
	m.mu.Unlock()
	if unchangedSeq != 1 {
		t.Fatalf("dedup update bumped sequence to %d, want 1", unchangedSeq)
	}
	time.Sleep(50 * time.Millisecond)
	if tty.buf.Len() != 0 {
		t.Fatalf("dedup update rendered: %q", tty.buf.String())
	}
}

// TestSixelPayloadCacheHit verifies a memoized payload is replayed as a bare
// burst (no decode/encode pipeline spawned) when the same thumbnail is
// requested at the same pane size again.
func TestSixelPayloadCacheHit(t *testing.T) {
	scr := tcell.NewSimulationScreen("")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer scr.Fini()

	rect := Rect{X: 1, Y: 2, W: 38, H: 25}
	path := "/tmp/thumb.png"
	m := &Manager{
		screen:        scr,
		renderer:      RendererSixel,
		cellW:         10,
		cellH:         20,
		sequence:      3,
		region:        Rect{X: 0, Y: 2, W: 40, H: 25},
		sixelCache:    map[string][]byte{},
		itemKey:       "A",
		itemPath:      path,
		inflight:      map[string]struct{}{},
		sixelDecoding: false,
	}
	payload := []byte("\x1bP0;0;8qcached\x1b\\")
	m.sixelCachePut(path, rect.W*m.cellW, rect.H*m.cellH, payload)

	// A cached render must post immediately without setting the decode gate.
	m.renderSixel(path, rect)

	m.mu.Lock()
	decoding := m.sixelDecoding
	m.mu.Unlock()
	if decoding {
		t.Fatalf("cache hit still started a decode pipeline")
	}

	burst := drainSixelBurst(t, scr)
	if burst == nil {
		return
	}
	if !bytes.Equal(burst.Payload, payload) {
		t.Fatalf("cache-hit payload mismatch: got %q, want %q", burst.Payload, payload)
	}
	if burst.Gen != 3 {
		t.Fatalf("cache-hit burst gen = %d, want 3", burst.Gen)
	}
}

// TestSixelWarmPalette verifies the shared encoder seeds a single reusable
// palette on first use, so subsequent encodes skip per-image median-cut.
func TestSixelWarmPalette(t *testing.T) {
	m := &Manager{sixelDither: false}
	img := makeTestImage(320, 200)

	if _, err := m.sixelEncode(img, 380, 500); err != nil {
		t.Fatalf("first encode: %v", err)
	}
	if m.sixelEnc == nil {
		t.Fatalf("shared encoder not created")
	}
	if len(m.sixelEnc.Palette) == 0 {
		t.Fatalf("palette not seeded on first encode")
	}
	if len(m.sixelEnc.Palette) > 255 {
		t.Fatalf("palette too large: %d > 255", len(m.sixelEnc.Palette))
	}

	// Second encode must reuse the same palette (the go-sixel fixedLUT path),
	// not re-seed it.
	first := m.sixelEnc.Palette
	if _, err := m.sixelEncode(makeTestImage(200, 120), 380, 500); err != nil {
		t.Fatalf("second encode: %v", err)
	}
	if m.sixelEnc.Palette != nil && len(m.sixelEnc.Palette) != len(first) {
		t.Fatalf("palette changed across encodes: %d -> %d colors", len(first), len(m.sixelEnc.Palette))
	}
}
