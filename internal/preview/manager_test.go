package preview

import (
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

func TestRebindScreenResetsPlacementState(t *testing.T) {
	oldScreen := tcell.NewSimulationScreen("")
	if err := oldScreen.Init(); err != nil {
		t.Fatalf("old screen init: %v", err)
	}
	defer oldScreen.Fini()

	newScreen := tcell.NewSimulationScreen("")
	if err := newScreen.Init(); err != nil {
		t.Fatalf("new screen init: %v", err)
	}
	defer newScreen.Fini()

	m := &Manager{
		screen:   oldScreen,
		region:   Rect{X: 1, Y: 2, W: 10, H: 6},
		itemKey:  "old",
		itemPath: "/tmp/old.jpg",
	}

	m.RebindScreen(newScreen)

	if m.screen != newScreen {
		t.Fatalf("screen not rebound")
	}
	if m.region != (Rect{}) {
		t.Fatalf("region = %#v, want zero value", m.region)
	}
	if m.itemKey != "" || m.itemPath != "" {
		t.Fatalf("item state not cleared: key=%q path=%q", m.itemKey, m.itemPath)
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
