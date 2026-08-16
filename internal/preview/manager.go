package preview

import (
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/gdamore/tcell/v2/terminfo"

	"github.com/user/gotube/internal/config"
)

// Timeouts and retry policy for the Kitty renderer. These exist because the
// app previously ran `kitten icat` synchronously on the main event loop: when
// Kitty's shared-memory IPC stalled, `cmd.Run()` blocked forever and the TUI
// froze (issue: gotube freeze/hang in Kitty terminal).
const (
	// kittyTimeout is the hard deadline for any kitten/icat subprocess. If the
	// process stalls we kill it and skip the thumbnail for that frame instead
	// of hanging the UI.
	kittyTimeout = 2 * time.Second

	// kittyRetryWait is how long the renderer stays marked as failed after a
	// kitten error, so a wedged process isn't re-spawned on every frame while
	// the user scrolls.
	kittyRetryWait = 10 * time.Second
)

type RendererKind string

const (
	RendererNone     RendererKind = ""
	RendererKitty    RendererKind = "kitty"
	RendererIterm    RendererKind = "imgcat"
	RendererUeberzug RendererKind = "ueberzugpp"
)

type Rect struct {
	X int
	Y int
	W int
	H int
}

type Item struct {
	Key          string
	ThumbnailURL string
}

// KittyBurst carries a completed kitty graphics write from the background
// renderer goroutine to the event loop. The event loop flushes it so the
// escape sequence can never interleave with tcell's own output during Show().
type KittyBurst struct {
	Gen          uint64 // manager sequence at spawn time; stale bursts are dropped
	CursorPrefix []byte // cursor-positioning escape (may be nil)
	Payload      []byte // complete stdout of the kitten process
}

type Manager struct {
	screen tcell.Screen
	tty    tcell.Tty
	ti     *terminfo.Terminfo

	renderer RendererKind
	cacheDir string

	httpClient *http.Client

	mu       sync.Mutex
	region   Rect
	itemKey  string
	itemPath string
	sequence uint64
	inflight map[string]struct{}
	closed   bool

	kittyCmd string
	ueberzug *ueberzugSession
	refresh  func()

	// Kitty renderer state (issue: gotube freeze/hang in Kitty terminal).
	// ttyMu serializes all raw TTY writes made by the preview manager so two
	// background kitty processes can never interleave their escape sequences.
	ttyMu          sync.Mutex
	kittyInFlight  bool      // true while a kitten process is running
	kittyFailUntil time.Time // grace period after a kitten failure
	kittyLastErr   error     // last kitten failure, kept for debugging

	activeCancel   context.CancelFunc
	prefetchCancel context.CancelFunc
}

func NewManager(screen tcell.Screen, cfg *config.PreviewConfig) (*Manager, error) {
	// Resolve cache directory: config override > default.
	cacheDir := previewCacheDir()
	if cfg != nil && cfg.CacheDir != "" {
		cacheDir = cfg.CacheDir
	}

	tty, ok := screen.Tty()
	if !ok {
		return &Manager{
			screen:     screen,
			renderer:   RendererNone,
			cacheDir:   cacheDir,
			httpClient: newPreviewHTTPClient(),
			inflight:   map[string]struct{}{},
		}, nil
	}

	// Determine renderer: config renderer > env var > auto-detect.
	renderer := chooseRenderer(cfg)
	m := &Manager{
		screen:     screen,
		tty:        tty,
		renderer:   renderer,
		cacheDir:   cacheDir,
		httpClient: newPreviewHTTPClient(),
		inflight:   map[string]struct{}{},
	}
	if ti, err := tcell.LookupTerminfo(os.Getenv("TERM")); err == nil {
		m.ti = ti
	}

	switch m.renderer {
	case RendererKitty:
		m.kittyCmd = chooseKittyCommand()
	case RendererUeberzug:
		sess, err := newUeberzugSession()
		if err != nil {
			m.renderer = RendererNone
		} else {
			m.ueberzug = sess
		}
	}

	if err := os.MkdirAll(m.cacheDir, 0o755); err != nil {
		return nil, err
	}

	// Use config max age if set, otherwise default 24h.
	maxAge := 24
	if cfg != nil && cfg.CacheMaxAge > 0 {
		maxAge = cfg.CacheMaxAge
	}
	CleanupCache(m.cacheDir, maxAge)

	return m, nil
}

func (m *Manager) Supported() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.renderer != RendererNone
}

func (m *Manager) SetRefreshHook(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh = fn
}

func (m *Manager) RebindScreen(screen tcell.Screen) {
	if screen == nil {
		return
	}

	tty, ok := screen.Tty()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.screen = screen
	if ok {
		m.tty = tty
	}
	m.region = Rect{}
	m.itemKey = ""
	m.itemPath = ""
	// Invalidate any in-flight kitty render so it doesn't paint onto the new
	// screen state (issue: kitty freeze/state desync).
	m.sequence++
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.activeCancel != nil {
		m.activeCancel()
		m.activeCancel = nil
	}
	if m.prefetchCancel != nil {
		m.prefetchCancel()
		m.prefetchCancel = nil
	}
	if m.ueberzug != nil {
		_ = m.ueberzug.Close()
		m.ueberzug = nil
	}
}

func (m *Manager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.sequence++
	m.itemKey = ""
	m.itemPath = ""
	if m.activeCancel != nil {
		m.activeCancel()
		m.activeCancel = nil
	}
	if m.prefetchCancel != nil {
		m.prefetchCancel()
		m.prefetchCancel = nil
	}
	m.clearLocked()
}

func (m *Manager) Update(item Item, rect Rect) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}

	key := itemKey(item)
	if key == "" || strings.TrimSpace(item.ThumbnailURL) == "" {
		m.Clear()
		return
	}

	path := m.cachePath(item.ThumbnailURL)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	changedRect := rect != m.region
	changedItem := key != m.itemKey || path != m.itemPath
	if changedRect {
		m.setRegionLocked(rect)
	}
	seq := m.sequence
	if !changedItem && !changedRect && fileExists(path) {
		m.mu.Unlock()
		return
	}
	m.itemKey = key
	m.itemPath = path
	m.sequence++
	seq = m.sequence
	if m.activeCancel != nil {
		m.activeCancel()
		m.activeCancel = nil
	}
	if fileExists(path) {
		m.mu.Unlock()
		m.renderCached(path, rect)
		return
	}
	if _, ok := m.inflight[path]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.activeCancel = cancel
	m.inflight[path] = struct{}{}
	m.mu.Unlock()

	go func(gen uint64, item Item, rect Rect, path string) {
		defer func() {
			m.mu.Lock()
			if m.activeCancel != nil && m.sequence == gen {
				m.activeCancel()
				m.activeCancel = nil
			}
			m.mu.Unlock()
		}()
		if err := m.ensureThumbnail(ctx, item.ThumbnailURL, path); err != nil {
			m.mu.Lock()
			delete(m.inflight, path)
			m.mu.Unlock()
			return
		}

		m.mu.Lock()
		delete(m.inflight, path)
		stillCurrent := !m.closed && m.sequence == gen && m.itemKey == itemKey(item) && m.itemPath == path && m.region == rect
		refresh := m.refresh
		m.mu.Unlock()
		if stillCurrent && refresh != nil {
			refresh()
		}
	}(seq, item, rect, path)
}

func (m *Manager) Prefetch(items []Item) {
	if len(items) == 0 {
		return
	}
	go func(items []Item) {
		m.mu.Lock()
		if m.prefetchCancel != nil {
			m.prefetchCancel()
			m.prefetchCancel = nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.prefetchCancel = cancel
		m.mu.Unlock()

		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for _, item := range items {
			item := item
			if ctx.Err() != nil {
				break
			}
			if strings.TrimSpace(item.ThumbnailURL) == "" {
				continue
			}
			path := m.cachePath(item.ThumbnailURL)
			if fileExists(path) {
				continue
			}
			m.mu.Lock()
			if m.closed {
				m.mu.Unlock()
				return
			}
			if _, ok := m.inflight[path]; ok {
				m.mu.Unlock()
				continue
			}
			m.inflight[path] = struct{}{}
			m.mu.Unlock()

			sem <- struct{}{}
			wg.Add(1)
			go func(item Item, path string) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := m.ensureThumbnail(ctx, item.ThumbnailURL, path); err != nil {
					m.mu.Lock()
					delete(m.inflight, path)
					m.mu.Unlock()
					return
				}
				m.mu.Lock()
				delete(m.inflight, path)
				stillCurrent := !m.closed && m.itemKey == itemKey(item) && m.itemPath == path
				refresh := m.refresh
				m.mu.Unlock()
				if stillCurrent && refresh != nil {
					refresh()
				}
			}(item, path)
		}
		wg.Wait()
		m.mu.Lock()
		if m.prefetchCancel != nil {
			m.prefetchCancel()
			m.prefetchCancel = nil
		}
		m.mu.Unlock()
	}(append([]Item(nil), items...))
}

func (m *Manager) cachePath(thumbURL string) string {
	sum := crc32.ChecksumIEEE([]byte(strings.TrimSpace(thumbURL)))
	name := fmt.Sprintf("%08x.jpg", sum)
	return filepath.Join(m.cacheDir, name)
}

func (m *Manager) ensureThumbnail(ctx context.Context, thumbURL, dest string) error {
	if fileExists(dest) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, thumbURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected thumbnail status: %d", resp.StatusCode)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (m *Manager) renderCached(path string, rect Rect) {
	m.mu.Lock()
	renderer := m.renderer
	tty := m.tty
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return
	}

	switch renderer {
	case RendererKitty:
		m.renderKitty(path, m.imageRect(rect))
	case RendererIterm:
		m.renderIterm(path, m.imageRect(rect), tty)
	case RendererUeberzug:
		m.renderUeberzug(path, m.imageRect(rect))
	}
}

func (m *Manager) renderKitty(path string, rect Rect) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	// During the failure grace period, skip rendering entirely instead of
	// stacking another doomed kitten process per frame (issue: freeze/hang).
	if time.Now().Before(m.kittyFailUntil) {
		m.mu.Unlock()
		return
	}
	// Coalesce: only one kitten render may be in flight. Rapid scrolling
	// otherwise spawns a subprocess per frame and floods the event queue
	// with refresh events.
	if m.kittyInFlight {
		m.mu.Unlock()
		return
	}
	if m.kittyCmd == "" {
		m.mu.Unlock()
		return
	}
	m.kittyInFlight = true
	gen := m.sequence
	kittyCmd := m.kittyCmd
	refresh := m.refresh
	m.mu.Unlock()

	// Kick the actual rendering into the background. render() never waits on
	// kitten, so keyboard input keeps flowing even if the process stalls.
	go m.renderKittyAsync(path, rect, gen, kittyCmd, refresh)
}

// renderKittyAsync runs kitten icat off the main thread, then hands the
// finished output to the event loop via a KittyBurst so it can be flushed
// without racing tcell.
func (m *Manager) renderKittyAsync(path string, rect Rect, gen uint64, kittyCmd string, refresh func()) {
	// Always clear the in-flight flag so later frames can retry.
	defer func() {
		m.mu.Lock()
		m.kittyInFlight = false
		m.mu.Unlock()
	}()

	args := []string{
		"icat",
		"--clear",
		// stream mode embeds the image in the escape-sequence stream instead
		// of Kitty's shared-memory IPC, which is the main stall source for
		// --transfer-mode=memory (issue: freeze/hang). Slightly more bytes
		// through the pty, but it cannot deadlock on IPC.
		"--transfer-mode=stream",
		"--unicode-placeholder",
		"--stdin=no",
		// Use absolute screen coordinates matching ueberzugpp's positioning.
		// @XxY is the top-left cell position on the terminal grid; the old
		// @0x0 hardcoded the image at (0,0), overlapping the header.
		fmt.Sprintf("--place=%dx%d@%dx%d", rect.W, rect.H, rect.X, rect.Y),
		path,
	}
	if kittyCmd == "icat" {
		args = args[1:]
	}

	out, err := m.runKittyAsync(kittyCmd, args)
	if err != nil {
		// Timeout or a missing/misbehaving kitten: mark the renderer as
		// temporarily failed and skip this frame. Silently, so the terminal
		// isn't polluted while the TUI is live.
		m.mu.Lock()
		m.kittyFailUntil = time.Now().Add(kittyRetryWait)
		m.kittyLastErr = err
		m.mu.Unlock()
		return
	}

	// The selection may have moved while kitten was running. If so, discard
	// the image and kick a redraw so the current selection gets rendered.
	m.mu.Lock()
	if m.closed || m.sequence != gen || m.itemPath != path || m.imageRect(m.region) != rect {
		refreshFn := m.refresh
		m.mu.Unlock()
		if refreshFn != nil {
			refreshFn()
		}
		return
	}
	m.mu.Unlock()

	// Build the burst: image payload only. The cursor-prefix approach is
	// unnecessary now that --place uses absolute screen coordinates.
	burst := &KittyBurst{Gen: gen, Payload: out}

	m.mu.Lock()
	screen := m.screen
	m.mu.Unlock()
	if screen == nil {
		return
	}

	// Post to the event loop. Retry briefly if the queue is momentarily full
	// (PostEvent drops events rather than blocking, and a dropped burst means
	// the image never paints, so a couple of quick retries are worth it).
	for i := 0; i < 5; i++ {
		if err := screen.PostEvent(tcell.NewEventInterrupt(burst)); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runKittyAsync runs a kitten/icat subprocess with a hard timeout and returns
// its stdout. It never blocks the caller beyond the timeout: if the process
// stalls, the context fires, the process is killed, and the error is returned
// so the caller can skip the frame instead of hanging the UI (issue: gotube
// freeze in Kitty terminal).
func (m *Manager) runKittyAsync(kittyCmd string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), kittyTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, kittyCmd, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard // keep kitten's complaints off the terminal
	cmd.Stdin = nil

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		// Hard timeout. exec.CommandContext kills the process when the
		// context expires (SIGKILL cannot be ignored), so draining the
		// runner goroutine here is prompt. We deliberately don't touch
		// cmd.Process ourselves: it isn't safe to access concurrently
		// with cmd.Run(), and CommandContext handles the kill.
		<-done
		return nil, ctx.Err()
	}

	return buf.Bytes(), nil
}

// FlushKitty emits a completed kitty graphics burst to the TTY. It must be
// called from the event loop thread so the write is serialized with tcell's
// own flushes (issue: uncoordinated TTY writes between tcell and Kitty).
// Bursts whose generation no longer matches are dropped — the region was
// cleared or the selection moved while the image was rendering.
func (m *Manager) FlushKitty(b *KittyBurst) {
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

// clearKittyGraphics sends the Kitty graphics "delete all" escape sequence
// directly to the TTY. It is a tiny synchronous write with no subprocess, so
// it can never stall on IPC the way `kitten icat --clear` could. It is used
// to reset Kitty's graphics state before clearing the region and before
// handing the terminal to mpv (issue: missing kitty graphics cleanup).
func (m *Manager) clearKittyGraphics() {
	m.ttyMu.Lock()
	defer m.ttyMu.Unlock()
	if m.tty == nil {
		return
	}
	// ESC _ G a=d ESC \ → delete all graphics placements (Kitty graphics
	// protocol spec: https://sw.kovidgoyal.net/kitty/graphics-protocol/).
	const deleteAll = "\x1b_Ga=d\x1b\\"
	_, _ = io.WriteString(m.tty, deleteAll)
}

func (m *Manager) renderIterm(path string, rect Rect, tty tcell.Tty) {
	m.clearRegion(rect)
	m.gotoCursor(tty, rect.X, rect.Y)

	cmd := exec.Command("imgcat", "-W", fmt.Sprintf("%d", rect.W), "-H", fmt.Sprintf("%d", rect.H), path)
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.Stdin = nil
	_ = cmd.Run()
}

func (m *Manager) clearRegion(rect Rect) {
	if m.screen == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	// LockRegion prevents tcell from redrawing characters in a rectangle.
	// For kitty it is useless (kitty images are GPU overlays, not cell
	// content) and it only interferes with tcell's rendering, so it is
	// skipped for the kitty renderer (issue: LockRegion ineffective for
	// kitty graphics).
	lockable := m.renderer != RendererKitty
	if lockable {
		m.screen.LockRegion(rect.X, rect.Y, rect.W, rect.H, false)
	}
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			m.screen.SetContent(x, y, ' ', nil, tcell.StyleDefault)
		}
	}
	m.screen.Show()
	if lockable {
		m.screen.LockRegion(rect.X, rect.Y, rect.W, rect.H, true)
	}
}

func (m *Manager) renderUeberzug(path string, rect Rect) {
	if m.ueberzug == nil {
		return
	}
	_ = m.ueberzug.Show(path, rect)
}

// gotoCursor moves the terminal cursor via a direct TTY write. Only the
// iTerm renderer uses this now; kitty's cursor placement is emitted together
// with the image payload in a single serialized burst (see renderKittyAsync).
func (m *Manager) gotoCursor(tty tcell.Tty, x, y int) {
	if tty == nil || m.ti == nil {
		return
	}
	_, _ = io.WriteString(tty, m.ti.TGoto(x, y))
}

func (m *Manager) imageRect(rect Rect) Rect {
	if rect.W <= 2 || rect.H <= 2 {
		return rect
	}
	insetX := 1
	if rect.W < 18 {
		insetX = 0
	}
	if rect.W <= insetX*2 {
		return rect
	}
	return Rect{
		X: rect.X + insetX,
		Y: rect.Y,
		W: rect.W - insetX*2,
		H: rect.H,
	}
}

func (m *Manager) clearLocked() {
	// For kitty, delete the GPU overlays first via a raw (non-blocking)
	// escape, then clear the cells, so the background redraw never fights a
	// stale image (issue: missing kitty graphics cleanup).
	if m.renderer == RendererKitty {
		m.clearKittyGraphics()
	}
	m.clearRegion(m.region)
	switch m.renderer {
	case RendererUeberzug:
		if m.ueberzug != nil {
			_ = m.ueberzug.Clear()
		}
	}
}

func (m *Manager) setRegionLocked(rect Rect) {
	if m.region == rect {
		return
	}
	if m.screen == nil {
		m.region = rect
		return
	}
	// LockRegion only makes sense for renderers whose images live in cell
	// content (ueberzug/iterm). Kitty images are GPU overlays, so locking the
	// region buys nothing and can fight tcell's own redraws (issue:
	// LockRegion ineffective for kitty graphics).
	lockable := m.renderer != RendererKitty
	if lockable && m.region.W > 0 && m.region.H > 0 {
		m.screen.LockRegion(m.region.X, m.region.Y, m.region.W, m.region.H, false)
	}
	m.region = rect
	if lockable && rect.W > 0 && rect.H > 0 {
		m.screen.LockRegion(rect.X, rect.Y, rect.W, rect.H, true)
	}
}

func previewCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "gotube", "preview_images")
}

func itemKey(item Item) string {
	if strings.TrimSpace(item.Key) != "" {
		return item.Key
	}
	return item.ThumbnailURL
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// chooseRenderer resolves the renderer with this priority:
//  1. config.Preview.Renderer (if set and not "auto")
//  2. IMAGE_RENDERER env var
//  3. automatic detection
func chooseRenderer(cfg *config.PreviewConfig) RendererKind {
	if cfg != nil {
		switch cfg.Renderer {
		case "kitty":
			if hasKittySupport() {
				return RendererKitty
			}
		case "imgcat", "iterm", "iterm2":
			if hasItermSupport() {
				return RendererIterm
			}
		case "ueberzugpp", "ueberzug":
			if commandExists("ueberzugpp") {
				return RendererUeberzug
			}
		case "none", "off", "false", "disabled":
			return RendererNone
		}
	}
	return detectRenderer()
}

func detectRenderer() RendererKind {
	explicit := strings.ToLower(strings.TrimSpace(os.Getenv("IMAGE_RENDERER")))
	switch explicit {
	case "", "auto", "detect", "ueberzugpp":
		// gotube-v2 prefers kitty or iTerm when detected, even if ueberzugpp is configured.
	default:
		if explicit != "" {
			switch explicit {
			case "kitty", "kitten", "icat":
				if hasKittySupport() {
					return RendererKitty
				}
			case "imgcat", "iterm", "iterm2":
				if hasItermSupport() {
					return RendererIterm
				}
			case "none", "off", "false":
				return RendererNone
			}
		}
	}

	if hasKittySupport() {
		return RendererKitty
	}
	if hasItermSupport() {
		return RendererIterm
	}
	if commandExists("ueberzugpp") {
		return RendererUeberzug
	}
	return RendererNone
}

func hasKittySupport() bool {
	term := strings.ToLower(os.Getenv("TERM"))
	hasKitty := os.Getenv("KITTY_WINDOW_ID") != "" || strings.Contains(term, "kitty")
	return hasKitty && (commandExists("kitten") || commandExists("icat") || commandExists("kitty"))
}

func hasItermSupport() bool {
	return os.Getenv("ITERM_SESSION_ID") != "" && commandExists("imgcat")
}

func chooseKittyCommand() string {
	switch {
	case commandExists("kitten"):
		return "kitten"
	case commandExists("icat"):
		return "icat"
	case commandExists("kitty"):
		return "kitty"
	default:
		return ""
	}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func newPreviewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:    10,
			IdleConnTimeout: 60 * time.Second,
		},
	}
}

const (
	maxCacheSize  = 500 * 1024 * 1024 // 500 MB max cache
	maxCacheFiles = 2000              // 2000 files max
)

func CleanupCache(dir string, maxAgeHours int) {
	if maxAgeHours <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Duration(maxAgeHours) * time.Hour)

	type entryInfo struct {
		name string
		info os.FileInfo
	}
	var aged []entryInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		} else {
			aged = append(aged, entryInfo{name: entry.Name(), info: info})
		}
	}

	// Enforce max file count (oldest first)
	if len(aged) > maxCacheFiles {
		sort.Slice(aged, func(i, j int) bool {
			return aged[i].info.ModTime().Before(aged[j].info.ModTime())
		})
		for _, e := range aged[maxCacheFiles:] {
			_ = os.Remove(filepath.Join(dir, e.name))
		}
		aged = aged[:maxCacheFiles]
	}

	// Enforce max total size (oldest first)
	var totalSize int64
	for _, e := range aged {
		totalSize += e.info.Size()
	}
	if totalSize > maxCacheSize {
		sort.Slice(aged, func(i, j int) bool {
			return aged[i].info.ModTime().Before(aged[j].info.ModTime())
		})
		for _, e := range aged {
			if totalSize <= maxCacheSize {
				break
			}
			totalSize -= e.info.Size()
			_ = os.Remove(filepath.Join(dir, e.name))
		}
	}
}
