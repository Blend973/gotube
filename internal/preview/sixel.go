package preview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-sixel"
	"github.com/soniakeys/quant/median"
	"golang.org/x/term"

	"github.com/user/gotube/internal/config"
)

// Sixel renderer timeouts and guards. The design mirrors the Kitty renderer:
// all expensive work (decode, resize, quantization, encoding) happens on a
// background goroutine, the finished escape sequence is handed to the event
// loop as a SixelBurst, and only the event loop may commit it to the TTY.
//
// The invariant we enforce for every renderer is the one gotube now applies
// to kitty: a broken or slow renderer must cause a missing preview, never a
// frozen application.
const (
	// sixelRetryWait is how long the renderer stays marked as failed after an
	// encode error, so a pathological image isn't re-quantized on every frame
	// while the user scrolls.
	sixelRetryWait = 10 * time.Second

	// maxSixelPayload bounds the TTY write. A giant escape sequence is itself
	// a latency source; if a thumbnail somehow encodes to more than this we
	// drop it rather than block the event loop flushing it.
	maxSixelPayload = 2 * 1024 * 1024 // 2 MiB

	// sixelCacheMaxEntries / sixelCacheMaxBytes bound the in-memory payload
	// memo. A typical thumbnail payload is tens of KB, so this comfortably
	// covers a whole scroll of revisits without growing unbounded.
	sixelCacheMaxEntries = 64
	sixelCacheMaxBytes   = 16 * 1024 * 1024 // 16 MiB
)

var errSixelPayloadTooLarge = errors.New("sixel payload exceeds maxSixelPayload")

// SixelBurst carries a completed DECSIXEL escape sequence from the background
// encoder goroutine to the event loop. The event loop flushes it through
// FlushSixel so the escape sequence can never interleave with tcell's own
// output during Show().
type SixelBurst struct {
	// Gen is the manager sequence at spawn time; stale bursts are dropped.
	Gen uint64

	// CursorPrefix positions the terminal cursor at the preview origin before
	// the payload, so the image lands in the right cell rectangle.
	CursorPrefix []byte

	// Payload is the complete, self-contained sixel escape sequence (ESC P q
	// ... ESC \). It was encoded into a memory buffer, never written directly.
	Payload []byte
}

// renderSixel schedules the sixel pipeline on a background goroutine and
// returns immediately. It must never run the encoder (or write to the TTY) on
// the caller's goroutine — that is the exact mistake that would reintroduce
// the UI freeze the kitty renderer was built to eliminate.
func (m *Manager) renderSixel(path string, rect Rect) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	// During the failure grace period, skip rendering entirely instead of
	// re-quantizing a pathological image per frame.
	if time.Now().Before(m.sixelFailUntil) {
		m.mu.Unlock()
		return
	}
	cellW, cellH := m.cellW, m.cellH
	gen := m.sequence
	m.mu.Unlock()

	maxW, maxH := rect.W*cellW, rect.H*cellH
	if payload, ok := m.sixelCacheGet(path, maxW, maxH); ok {
		m.postSixel(&SixelBurst{Gen: gen, CursorPrefix: m.gotoPrefix(rect), Payload: payload})
		return
	}

	// Coalesce: only one decode stage may run at a time. Rapid scrolling
	// otherwise spawns a decode per frame and saturates the CPU. The dropped
	// render is recorded so Update() re-renders it once the current pipeline
	// frees the gate (issue: images missing on fast selection).
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if time.Now().Before(m.sixelFailUntil) {
		m.mu.Unlock()
		return
	}
	if m.sixelDecoding {
		m.pendingRender = true
		m.mu.Unlock()
		return
	}
	m.sixelDecoding = true
	m.mu.Unlock()

	go m.sixelPipeline(path, rect, gen, cellW, cellH)
}

// sixelPipeline runs the two-stage render off the event loop. Stage 1 decodes
// and resizes, which overlaps another image's encode (the depth-1 pipeline);
// stage 2 encodes, serialized via sixelEncodeMu so the shared encoder/buffer
// is never raced. The finished payload is handed to the event loop as a
// SixelBurst. The generation is validated before encoding and again in
// FlushSixel immediately before the TTY write, because the selection can move
// at any point.
func (m *Manager) sixelPipeline(path string, rect Rect, gen uint64, cellW, cellH int) {
	maxW, maxH := rect.W*cellW, rect.H*cellH

	// Stage 1: decode + resize. Uses otherwise-idle CPU while another image
	// encodes, so wall time drops without adding total work.
	img, err := decodeImage(context.Background(), path)
	if err == nil {
		img = fitImage(img, maxW, maxH)
	}

	// Free the decode slot now so the next selection can start decoding while
	// this goroutine handles the encode stage. This is what makes the two
	// stages overlap, bounded to one decoder + one encoder at most.
	m.mu.Lock()
	m.sixelDecoding = false
	m.mu.Unlock()

	if err != nil {
		m.markSixelFailed()
		return
	}

	// Stage 2: encode. Serialized so the shared encoder/buffer is never
	// accessed concurrently; a stale pipeline bails here before spending CPU.
	m.sixelEncodeMu.Lock()
	defer m.sixelEncodeMu.Unlock()

	m.mu.Lock()
	if m.closed || m.sequence != gen || m.itemPath != path || m.imageRect(m.region) != rect {
		if !m.closed {
			m.pendingRender = true
		}
		refreshFn := m.refresh
		m.mu.Unlock()
		if refreshFn != nil {
			refreshFn()
		}
		return
	}
	// rect is already the inset image rect from renderCached (it went through
	// imageRect there exactly once). Do NOT re-apply imageRect — that double
	// insets X and lets the image bleed over the pane's right border onto the
	// results list. This position matches kitty/ueberzugpp.
	prefix := m.gotoPrefix(rect)
	m.mu.Unlock()

	payload, err := m.sixelEncode(img, maxW, maxH)
	if err != nil {
		m.markSixelFailed()
		return
	}

	m.mu.Lock()
	stale := m.closed || m.sequence != gen || m.itemPath != path
	if stale {
		if !m.closed {
			m.pendingRender = true
		}
		refreshFn := m.refresh
		m.mu.Unlock()
		if refreshFn != nil {
			refreshFn()
		}
		return
	}
	m.mu.Unlock()

	m.sixelCachePut(path, maxW, maxH, payload)
	m.postSixel(&SixelBurst{Gen: gen, CursorPrefix: prefix, Payload: payload})
}

// postSixel hands a finished burst to the event loop. Retry briefly if the
// queue is momentarily full (PostEvent drops events rather than blocking, and
// a dropped burst means the image never paints, so a couple of quick retries
// are worth it).
func (m *Manager) postSixel(burst *SixelBurst) {
	m.mu.Lock()
	screen := m.screen
	m.mu.Unlock()
	if screen == nil {
		return
	}
	for i := 0; i < 5; i++ {
		if err := screen.PostEvent(tcell.NewEventInterrupt(burst)); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// markSixelFailed enters the failure grace period so a pathological image
// isn't re-processed on every frame while the user scrolls.
func (m *Manager) markSixelFailed() {
	m.mu.Lock()
	m.sixelFailUntil = time.Now().Add(sixelRetryWait)
	m.mu.Unlock()
}

// sixelEncode quantizes and encodes img using the shared encoder. The encoder
// and its palette lookup table are built once and reused across encodes, so
// every encode after the first skips the per-image median-cut quantization
// (the dominant cost) that a fresh encoder would pay. Concurrency is the
// caller's responsibility: the pipeline serializes encodes via sixelEncodeMu.
func (m *Manager) sixelEncode(img image.Image, maxW, maxH int) ([]byte, error) {
	if m.sixelEnc == nil {
		m.sixelBuf = &bytes.Buffer{}
		m.sixelEnc = sixel.NewEncoder(m.sixelBuf)
	}
	enc := m.sixelEnc
	enc.Width = maxW
	enc.Height = maxH
	enc.Dither = m.sixelDither
	enc.Colors = 0 // 256 colors

	// Seed the shared palette from the first image so all later encodes reuse
	// it (via go-sixel's cached palette lookup table) instead of computing an
	// adaptive median-cut palette per image.
	if enc.Palette == nil {
		if pal := buildSixelPalette(img); len(pal) <= 255 {
			enc.Palette = pal
		}
	}

	m.sixelBuf.Reset()
	if err := enc.Encode(img); err != nil {
		return nil, err
	}
	out := bytes.Clone(m.sixelBuf.Bytes())
	if len(out) > maxSixelPayload {
		return nil, errSixelPayloadTooLarge
	}
	return out, nil
}

// buildSixelPalette computes a 255-color palette from img via median cut. It
// runs once to seed the shared encoder palette; subsequent encodes reuse it.
func buildSixelPalette(img image.Image) color.Palette {
	pal := median.Quantizer(0).Quantize(make(color.Palette, 0, 255), img)
	if len(pal) > 255 {
		pal = pal[:255]
	}
	return pal
}

// sixelCacheKey identifies a cached payload by source thumbnail and target
// pixel size; the same URL at the same pane size always yields the same bytes.
func (m *Manager) sixelCacheKey(path string, maxW, maxH int) string {
	return path + "|" + strconv.Itoa(maxW) + "x" + strconv.Itoa(maxH)
}

// sixelCacheGet returns the memoized payload for a path/size, if present.
func (m *Manager) sixelCacheGet(path string, maxW, maxH int) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sixelCache == nil {
		return nil, false
	}
	p, ok := m.sixelCache[m.sixelCacheKey(path, maxW, maxH)]
	return p, ok
}

// sixelCachePut memoizes a payload, evicting the oldest entries when the
// cache exceeds its count/byte bounds.
func (m *Manager) sixelCachePut(path string, maxW, maxH int, payload []byte) {
	key := m.sixelCacheKey(path, maxW, maxH)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sixelCache == nil {
		m.sixelCache = map[string][]byte{}
	}
	if _, ok := m.sixelCache[key]; ok {
		return
	}
	m.sixelCache[key] = payload
	m.sixelCacheOrder = append(m.sixelCacheOrder, key)
	m.sixelCacheBytes += len(payload)
	for len(m.sixelCacheOrder) > sixelCacheMaxEntries || m.sixelCacheBytes > sixelCacheMaxBytes {
		oldest := m.sixelCacheOrder[0]
		m.sixelCacheOrder = m.sixelCacheOrder[1:]
		if p, ok := m.sixelCache[oldest]; ok {
			m.sixelCacheBytes -= len(p)
			delete(m.sixelCache, oldest)
		}
	}
}

// FlushSixel emits a completed sixel burst to the TTY. It must be called from
// the event loop thread so the write is serialized with tcell's own flushes.
// Bursts whose generation no longer matches are dropped — the region was
// cleared or the selection moved while the image was encoding.
func (m *Manager) FlushSixel(b *SixelBurst) {
	if b == nil {
		return
	}
	m.mu.Lock()
	closed := m.closed
	stale := m.sequence != b.Gen
	m.mu.Unlock()
	if closed || stale {
		return
	}

	m.ttyMu.Lock()
	defer m.ttyMu.Unlock()
	if m.tty == nil {
		return
	}
	if len(b.CursorPrefix) > 0 {
		_, _ = m.tty.Write(b.CursorPrefix)
	}
	if len(b.Payload) > 0 {
		_, _ = m.tty.Write(b.Payload)
	}
}

// gotoPrefix builds the cursor-positioning escape that places the terminal
// cursor at the top-left of rect, so the following sixel image lands in the
// right cell rectangle. Sixel images are terminal-side graphics anchored at
// the cursor, so this must be emitted together with the payload in a single
// serialized burst.
func (m *Manager) gotoPrefix(rect Rect) []byte {
	if m.ti == nil || rect.X < 0 || rect.Y < 0 {
		return nil
	}
	return []byte(m.ti.TGoto(rect.X, rect.Y))
}

// UsesSixel reports whether the resolved renderer (config > env > auto) will
// be the sixel renderer. The TUI uses this to decide whether to spend a
// startup round-trip querying the terminal cell size.
func UsesSixel(cfg *config.PreviewConfig) bool {
	return chooseRenderer(cfg) == RendererSixel
}

// QueryCellSize asks the terminal for its character cell size in pixels via
// the DEC "CSI 16 t" request (xterm report-character-cell-size-in-pixels),
// returning the cell width and height. It must be called before the TUI takes
// over the terminal: once tcell's input loop is running it is blocked in a
// read() that would steal the response, and tcell's parser discards the
// "CSI 6 ; h ; w t" report. It is a short, bounded call that returns (0,0) if
// the terminal doesn't answer, so the caller can fall back to a default.
func QueryCellSize() (cellW, cellH int) {
	fd := int(os.Stdin.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return 0, 0 // stdin isn't a terminal
	}
	if _, err := term.MakeRaw(fd); err != nil {
		return 0, 0
	}
	defer func() { _ = term.Restore(fd, state) }()

	return queryCellSizeReport(os.Stdin, os.Stdout, cellSizeQueryTimeout)
}

// cellSizeQueryTimeout bounds how long QueryCellSize will wait for a terminal
// that doesn't answer "CSI 16 t". On terminals that do answer, the report
// arrives in milliseconds; this only delays startup on terminals that stay
// silent.
const cellSizeQueryTimeout = 500 * time.Millisecond

// queryCellSizeReport writes the "CSI 16 t" query to w and reads the answer
// from r. It is separated from QueryCellSize so it can be unit-tested against
// pipes without touching the real terminal or termios. Returns (0,0) on any
// failure or timeout.
func queryCellSizeReport(r io.Reader, w io.Writer, timeout time.Duration) (cellW, cellH int) {
	if _, err := io.WriteString(w, "\x1b[16t"); err != nil {
		return 0, 0
	}

	// Read the answer. The terminal replies contiguously, ending in 't', so we
	// stop as soon as we've seen one — but keep the goroutine bounded.
	resp := make(chan []byte, 1)
	go func() {
		var buf []byte
		tmp := make([]byte, 64)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				if bytes.Contains(buf, []byte{'t'}) {
					break
				}
			}
			if err != nil || len(buf) > 256 {
				break
			}
		}
		resp <- buf
	}()

	select {
	case buf := <-resp:
		w, h, ok := parseCellSizeReport(buf)
		if ok {
			return w, h
		}
	case <-time.After(timeout):
	}
	return 0, 0
}

// parseCellSizeReport parses the answer to "CSI 16 t", which is
// "ESC [ 6 ; cellheight ; cellwidth t". Returns cell width and height.
func parseCellSizeReport(buf []byte) (cellW, cellH int, ok bool) {
	i := bytes.Index(buf, []byte("\x1b["))
	if i < 0 {
		return 0, 0, false
	}
	body := buf[i+2:]
	end := bytes.IndexByte(body, 't')
	if end < 0 {
		return 0, 0, false
	}
	body = body[:end]
	parts := strings.Split(string(body), ";")
	if len(parts) != 3 || parts[0] != "6" {
		return 0, 0, false
	}
	h, errH := strconv.Atoi(parts[1])
	w, errW := strconv.Atoi(parts[2])
	if errH != nil || errW != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// resolveCellPixels returns the terminal cell pixel dimensions used to size a
// sixel image from a cell-based rect. Priority: explicit config
// (cell_width/cell_height) > detected value from QueryCellSize > a conservative
// 8x16 default (common for foot, wezterm, xterm). Getting this right is what
// makes the image occupy exactly the pane's cell rect instead of overflowing.
func resolveCellPixels(cfg *config.PreviewConfig, detectedW, detectedH int) (cellW, cellH int) {
	cellW, cellH = 8, 16
	if cfg != nil {
		if cfg.CellWidth > 0 {
			cellW = cfg.CellWidth
		}
		if cfg.CellHeight > 0 {
			cellH = cfg.CellHeight
		}
	}
	// Config not set on an axis → use the detected value.
	if (cfg == nil || cfg.CellWidth <= 0) && detectedW > 0 {
		cellW = detectedW
	}
	if (cfg == nil || cfg.CellHeight <= 0) && detectedH > 0 {
		cellH = detectedH
	}
	return cellW, cellH
}

// hasSixelSupport reports whether the current terminal advertises Sixel
// support. This is deliberately conservative heuristic detection based on
// well-known Sixel-capable terminal names; the reliable path is explicit
// configuration (preview.renderer = "sixel"), and this auto-detection is a
// convenience fallback.
func hasSixelSupport() bool {
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	if term == "" {
		return false
	}
	known := []string{
		"foot", "mlterm", "wezterm", "contour", "notcurses",
		"st-256color", // st with the sixel patch
		"yaft", "aterm",
	}
	for _, name := range known {
		if strings.Contains(term, name) {
			return true
		}
	}
	// xterm-family terminals with Sixel enabled, and Windows Terminal
	// (1.22+) which gained Sixel support.
	if strings.Contains(term, "xterm") || strings.Contains(term, "windows-terminal") {
		return os.Getenv("WT_SESSION") != "" || os.Getenv("XTERM_VERSION") != ""
	}
	return false
}
