package preview

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/gdamore/tcell/v2"
)

// iTerm2 renderer. This was originally a synchronous `cmd.Run()` on the event
// loop, which meant a stalled imgcat could freeze the UI exactly like the old
// kitty path did. It now follows the same async contract as kitty and sixel:
// the subprocess runs in the background with a hard timeout, and its output
// is flushed from the event loop through ItermBurst so it can never race
// tcell's own writes.
const (
	// itermTimeout is the hard deadline for an imgcat subprocess. If it
	// stalls, we kill it and skip the thumbnail for that frame instead of
	// hanging the UI.
	itermTimeout = 2 * time.Second

	// itermRetryWait is how long the renderer stays marked as failed after an
	// imgcat error, so a wedged process isn't re-spawned on every frame.
	itermRetryWait = 10 * time.Second
)

// ItermBurst carries a completed imgcat escape sequence from the background
// goroutine to the event loop for serialized flushing.
type ItermBurst struct {
	Gen          uint64
	CursorPrefix []byte
	Payload      []byte
}

// renderIterm schedules an imgcat render on a background goroutine and
// returns immediately. It never waits on the subprocess from the caller's
// goroutine, so a slow imgcat cannot block the event loop.
func (m *Manager) renderIterm(path string, rect Rect) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if time.Now().Before(m.itermFailUntil) {
		m.mu.Unlock()
		return
	}
	if m.itermInFlight {
		// Coalesced away. Record it so Update() re-renders the current item
		// once the in-flight render frees the gate, and the refresh it kicks
		// afterward isn't swallowed by the unchanged-item dedup.
		m.pendingRender = true
		m.mu.Unlock()
		return
	}
	m.itermInFlight = true
	gen := m.sequence
	m.mu.Unlock()

	go m.renderItermAsync(path, rect, gen)
}

// renderItermAsync runs imgcat off the main thread, captures its output into
// memory, then hands the finished payload to the event loop via an
// ItermBurst. The generation is validated before posting and again in
// FlushIterm right before the TTY write.
func (m *Manager) renderItermAsync(path string, rect Rect, gen uint64) {
	defer func() {
		m.mu.Lock()
		m.itermInFlight = false
		m.mu.Unlock()
	}()

	payload, err := m.runItermAsync(path, rect)
	if err != nil {
		m.mu.Lock()
		m.itermFailUntil = time.Now().Add(itermRetryWait)
		m.mu.Unlock()
		return
	}

	// The selection may have moved while imgcat was running.
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
	// rect is already the inset image rect from renderCached (imageRect applied
	// exactly once). Re-applying it would double-inset X and misplace the image.
	prefix := m.gotoPrefix(rect)
	m.mu.Unlock()

	burst := &ItermBurst{Gen: gen, CursorPrefix: prefix, Payload: payload}

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

// runItermAsync runs imgcat with a hard timeout and returns its stdout. It
// never blocks the caller beyond the timeout: if the process stalls, the
// context fires, the process is killed, and the error is returned so the
// caller can skip the frame instead of hanging the UI.
func (m *Manager) runItermAsync(path string, rect Rect) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), itermTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "imgcat",
		"-W", fmt.Sprintf("%d", rect.W),
		"-H", fmt.Sprintf("%d", rect.H),
		path,
	)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard // keep imgcat's complaints off the terminal
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
		<-done
		return nil, ctx.Err()
	}
	return buf.Bytes(), nil
}

// FlushIterm emits a completed imgcat burst to the TTY. It must be called
// from the event loop thread so the write is serialized with tcell's own
// flushes. Bursts whose generation no longer matches are dropped.
func (m *Manager) FlushIterm(b *ItermBurst) {
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
