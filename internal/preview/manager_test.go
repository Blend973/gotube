package preview

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestImageRectKeepsTopAligned(t *testing.T) {
	m := &Manager{}
	got := m.imageRect(Rect{X: 0, Y: 2, W: 30, H: 12})
	if got.Y != 2 {
		t.Fatalf("imageRect Y = %d, want 2", got.Y)
	}
	if got.X != 1 {
		t.Fatalf("imageRect X = %d, want 1", got.X)
	}
	if got.W != 28 {
		t.Fatalf("imageRect W = %d, want 28", got.W)
	}
	if got.H != 12 {
		t.Fatalf("imageRect H = %d, want 12", got.H)
	}
}

func TestUpdateCancelsPreviousFetch(t *testing.T) {
	started := make(chan string, 2)
	canceled := make(chan string, 2)

	m := &Manager{
		cacheDir: t.TempDir(),
		httpClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				started <- req.URL.String()
				<-req.Context().Done()
				canceled <- req.URL.String()
				return nil, req.Context().Err()
			}),
		},
		inflight: map[string]struct{}{},
	}
	defer m.Close()

	m.Update(Item{Key: "one", ThumbnailURL: "https://example.com/one.jpg"}, Rect{X: 0, Y: 0, W: 20, H: 10})
	waitForURL(t, started, "https://example.com/one.jpg")

	m.Update(Item{Key: "two", ThumbnailURL: "https://example.com/two.jpg"}, Rect{X: 0, Y: 0, W: 20, H: 10})
	waitForURL(t, canceled, "https://example.com/one.jpg")
}

func TestPrefetchCancelsPreviousBatch(t *testing.T) {
	started := make(chan string, 2)
	canceled := make(chan string, 2)

	m := &Manager{
		cacheDir: t.TempDir(),
		httpClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				started <- req.URL.String()
				<-req.Context().Done()
				canceled <- req.URL.String()
				return nil, req.Context().Err()
			}),
		},
		inflight: map[string]struct{}{},
	}
	defer m.Close()

	m.Prefetch([]Item{{Key: "one", ThumbnailURL: "https://example.com/one.jpg"}})
	waitForURL(t, started, "https://example.com/one.jpg")

	m.Prefetch([]Item{{Key: "two", ThumbnailURL: "https://example.com/two.jpg"}})
	waitForURL(t, canceled, "https://example.com/one.jpg")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func waitForURL(t *testing.T, ch <-chan string, want string) {
	t.Helper()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-ch:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

// fakeTty is a minimal tcell.Tty that records writes, so tests can verify
// what the kitty renderer would send to the terminal.
type fakeTty struct {
	buf bytes.Buffer
}

func (f *fakeTty) Start() error                          { return nil }
func (f *fakeTty) Stop() error                           { return nil }
func (f *fakeTty) Drain() error                          { return nil }
func (f *fakeTty) NotifyResize(func())                   {}
func (f *fakeTty) WindowSize() (tcell.WindowSize, error) { return tcell.WindowSize{}, nil }
func (f *fakeTty) Read(p []byte) (int, error)            { return 0, nil }
func (f *fakeTty) Write(p []byte) (int, error)           { return f.buf.Write(p) }
func (f *fakeTty) Close() error                          { return nil }

// TestRunKittyAsyncTimeout verifies that a stalled kitten process is killed
// after the hard timeout instead of blocking forever (the root cause of the
// freeze: `cmd.Run()` on the main thread with no timeout).
func TestRunKittyAsyncTimeout(t *testing.T) {
	m := &Manager{}
	start := time.Now()
	_, err := m.runKittyAsync("sh", []string{"-c", "exec sleep 30"})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline exceeded", err)
	}
	// Give the timeout a generous ceiling: it must be nowhere near the
	// 30s sleep, proving the process was killed rather than waited on.
	if elapsed > 10*time.Second {
		t.Fatalf("runKittyAsync took %v, timeout clearly not enforced", elapsed)
	}
	if elapsed < 2*time.Second {
		t.Fatalf("runKittyAsync returned after %v, before the 2s timeout", elapsed)
	}
}

// TestRunKittyAsyncSuccess verifies the happy path still returns output.
func TestRunKittyAsyncSuccess(t *testing.T) {
	m := &Manager{}
	out, err := m.runKittyAsync("sh", []string{"-c", "printf 'abc'"})
	if err != nil {
		t.Fatalf("runKittyAsync error: %v", err)
	}
	if string(out) != "abc" {
		t.Fatalf("out = %q, want %q", out, "abc")
	}
}

// TestFlushKittyWrites verifies a current-generation burst is written as a
// single ordered sequence (cursor prefix then payload).
func TestFlushKittyWrites(t *testing.T) {
	f := &fakeTty{}
	m := &Manager{tty: f, sequence: 7}
	m.FlushKitty(&KittyBurst{
		Gen:          7,
		CursorPrefix: []byte("\x1b[1;1H"),
		Payload:      []byte("\x1b_G...\x1b\\"),
	})
	want := "\x1b[1;1H\x1b_G...\x1b\\"
	if f.buf.String() != want {
		t.Fatalf("tty write = %q, want %q", f.buf.String(), want)
	}
}

// TestFlushKittyDropsStale verifies bursts from an older generation (region
// cleared or selection moved while kitten was rendering) are discarded.
func TestFlushKittyDropsStale(t *testing.T) {
	f := &fakeTty{}
	m := &Manager{tty: f, sequence: 9}
	m.FlushKitty(&KittyBurst{Gen: 8, Payload: []byte("stale")})
	if f.buf.Len() != 0 {
		t.Fatalf("stale burst was written: %q", f.buf.String())
	}

	// Closed managers must also drop bursts (app quit while kitten was
	// rendering).
	f2 := &fakeTty{}
	m2 := &Manager{tty: f2, sequence: 9, closed: true}
	m2.FlushKitty(&KittyBurst{Gen: 9, Payload: []byte("late")})
	if f2.buf.Len() != 0 {
		t.Fatalf("burst written after close: %q", f2.buf.String())
	}
}
