package tui

import (
	"strings"
	"testing"
)

func TestTruncateByWidth(t *testing.T) {
	input := "🔍 GoTube › fullscreen mode"
	got := truncateByWidth(input, 12)
	if displayWidth(got) > 12 {
		t.Fatalf("truncateByWidth width=%d, want <= 12", displayWidth(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncateByWidth(%q) = %q, want ellipsis", input, got)
	}
}

func TestWrapTextRespectsWidth(t *testing.T) {
	lines := wrapText("alpha beta gamma delta", 10)
	if len(lines) < 2 {
		t.Fatalf("wrapText returned %d lines, want multiple lines", len(lines))
	}
	for i, line := range lines {
		if displayWidth(line) > 10 {
			t.Fatalf("line %d width=%d, want <= 10", i, displayWidth(line))
		}
	}
}

func TestPreviewPaneWidth(t *testing.T) {
	if got := previewPaneWidth(69); got == 0 {
		t.Fatalf("previewPaneWidth(69) = 0, want non-zero")
	}

	got := previewPaneWidth(120)
	if got < 24 {
		t.Fatalf("previewPaneWidth(120) = %d, want >= 24", got)
	}
	if got > 45 {
		t.Fatalf("previewPaneWidth(120) = %d, want <= 45", got)
	}
}

func TestPreviewPaneWidthSmallTerminal(t *testing.T) {
	got := previewPaneWidth(30)
	if got == 0 {
		t.Fatalf("previewPaneWidth(30) = 0, want preview pane to still exist")
	}
	if got < 8 {
		t.Fatalf("previewPaneWidth(30) = %d, want >= 8", got)
	}
}

func TestDialogRectStaysInResultsPaneWhenPreviewExists(t *testing.T) {
	totalW := 120
	boxX, _, boxW, _ := dialogRect(totalW, 40, 55, 16)
	previewW := previewPaneWidth(totalW)
	if previewW == 0 {
		t.Fatalf("previewPaneWidth(%d) = 0, want preview pane", totalW)
	}
	if boxX < previewW+1 {
		t.Fatalf("dialogRect placed popup at x=%d, want >= %d", boxX, previewW+1)
	}
	if boxX+boxW > totalW {
		t.Fatalf("dialogRect overflowed total width: x=%d w=%d total=%d", boxX, boxW, totalW)
	}
}

func TestDialogRectUsesFullWidthWhenNoPreview(t *testing.T) {
	boxX, _, boxW, _ := dialogRect(18, 12, 55, 16)
	if boxX < 0 {
		t.Fatalf("dialogRect x=%d, want non-negative", boxX)
	}
	if boxX+boxW > 18 {
		t.Fatalf("dialogRect overflowed narrow screen: x=%d w=%d", boxX, boxW)
	}
}
