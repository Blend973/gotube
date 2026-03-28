package tui

import (
	"strings"

	"github.com/rivo/uniseg"
)

func displayWidth(s string) int {
	return uniseg.StringWidth(s)
}

func truncateByWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if displayWidth(s) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return strings.Repeat(".", maxWidth)
	}

	limit := maxWidth - 3
	var b strings.Builder
	width := 0

	g := uniseg.NewGraphemes(s)
	for g.Next() {
		cluster := g.Str()
		clusterWidth := g.Width()
		if width+clusterWidth > limit {
			break
		}
		b.WriteString(cluster)
		width += clusterWidth
	}

	return b.String() + "..."
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return nil
	}

	paragraphs := strings.Split(text, "\n")
	lines := make([]string, 0, len(paragraphs))

	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}

		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}

		current := ""
		currentWidth := 0

		flushCurrent := func() {
			if current != "" {
				lines = append(lines, current)
				current = ""
				currentWidth = 0
			}
		}

		for _, word := range words {
			wordWidth := displayWidth(word)
			if wordWidth > width {
				flushCurrent()
				lines = append(lines, splitLongWord(word, width)...)
				continue
			}

			if current == "" {
				current = word
				currentWidth = wordWidth
				continue
			}

			if currentWidth+1+wordWidth <= width {
				current += " " + word
				currentWidth += 1 + wordWidth
				continue
			}

			flushCurrent()
			current = word
			currentWidth = wordWidth
		}

		flushCurrent()
	}

	return lines
}

func splitLongWord(word string, width int) []string {
	if width <= 0 {
		return nil
	}

	var lines []string
	var current strings.Builder
	currentWidth := 0

	g := uniseg.NewGraphemes(word)
	for g.Next() {
		cluster := g.Str()
		clusterWidth := g.Width()

		if currentWidth > 0 && currentWidth+clusterWidth > width {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
		}

		if currentWidth == 0 && clusterWidth > width {
			lines = append(lines, cluster)
			continue
		}

		current.WriteString(cluster)
		currentWidth += clusterWidth
	}

	if current.Len() > 0 {
		lines = append(lines, current.String())
	}

	return lines
}

func previewPaneWidth(totalWidth int) int {
	if totalWidth < 24 {
		return 0
	}

	previewW := totalWidth * 35 / 100
	if previewW < 12 {
		previewW = 12
	}

	maxPreviewW := totalWidth - 12
	if maxPreviewW < 0 {
		return 0
	}
	if previewW > maxPreviewW {
		previewW = maxPreviewW
	}
	if previewW < 8 {
		return 0
	}
	return previewW
}
