package tui

import "testing"

func TestWrapSelectionIndex(t *testing.T) {
	if got := wrapSelectionIndex(0, -1, 5); got != 4 {
		t.Fatalf("wrapSelectionIndex(0, -1, 5) = %d, want 4", got)
	}
	if got := wrapSelectionIndex(4, 1, 5); got != 0 {
		t.Fatalf("wrapSelectionIndex(4, 1, 5) = %d, want 0", got)
	}
	if got := wrapSelectionIndex(2, 1, 5); got != 3 {
		t.Fatalf("wrapSelectionIndex(2, 1, 5) = %d, want 3", got)
	}
}

func TestResultListMaxItems(t *testing.T) {
	if got := resultListMaxItems(10); got != 2 {
		t.Fatalf("resultListMaxItems(10) = %d, want 2", got)
	}
	if got := resultListMaxItems(3); got != 1 {
		t.Fatalf("resultListMaxItems(3) = %d, want 1", got)
	}
}
