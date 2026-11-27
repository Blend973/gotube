package tui

import (
	"strings"

	"github.com/rivo/uniseg"
)

func displayWidth(s string) int {
	return uniseg.StringWidth(s)
}

// graphemeRuneBoundaries returns rune offsets at which a user-perceived
// character starts or ends. Search editing stores runes, but cursor movement
// and deletion must not split combining sequences or emoji ZWJ sequences.
func graphemeRuneBoundaries(runes []rune) []int {
	boundaries := []int{0}
	graphemes := uniseg.NewGraphemes(string(runes))
	runeOffset := 0
	for graphemes.Next() {
		runeOffset += len([]rune(graphemes.Str()))
		boundaries = append(boundaries, runeOffset)
	}
	return boundaries
}

func previousGraphemeBoundary(runes []rune, pos int) int {
	previous := 0
	for _, boundary := range graphemeRuneBoundaries(runes) {
		if boundary >= pos {
			return previous
		}
		previous = boundary
	}
	return previous
}

func nextGraphemeBoundary(runes []rune, pos int) int {
	for _, boundary := range graphemeRuneBoundaries(runes) {
		if boundary > pos {
			return boundary
		}
	}
	return len(runes)
}

// searchViewport returns a grapheme-safe visible slice and the cursor's cell
// offset within it. scroll is measured in terminal cells and is snapped to a
// grapheme boundary before being stored back.
func searchViewport(runes []rune, cursor, maxWidth int, scroll *int) (string, int) {
	if maxWidth <= 0 {
		return "", 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}

	type cluster struct {
		text      string
		runeStart int
		runeEnd   int
		colStart  int
		colEnd    int
	}
	var clusters []cluster
	col := 0
	runeOffset := 0
	g := uniseg.NewGraphemes(string(runes))
	cursorCol := 0
	for g.Next() {
		part := g.Str()
		count := len([]rune(part))
		item := cluster{text: part, runeStart: runeOffset, runeEnd: runeOffset + count, colStart: col, colEnd: col + g.Width()}
		clusters = append(clusters, item)
		if item.runeEnd <= cursor {
			cursorCol = item.colEnd
		} else if item.runeStart < cursor {
			// A cursor inside a cluster is rendered at its trailing boundary.
			cursorCol = item.colEnd
		}
		runeOffset += count
		col = item.colEnd
	}
	if cursor == 0 {
		cursorCol = 0
	} else if cursor >= len(runes) {
		cursorCol = col
	}

	desiredScroll := *scroll
	if cursorCol > desiredScroll+maxWidth {
		desiredScroll = cursorCol - maxWidth
	}
	if cursorCol < desiredScroll {
		desiredScroll = cursorCol
	}
	if desiredScroll < 0 {
		desiredScroll = 0
	}
	actualScroll := col
	for _, item := range clusters {
		if item.colStart >= desiredScroll {
			actualScroll = item.colStart
			break
		}
		if item.colEnd >= desiredScroll {
			actualScroll = item.colEnd
			break
		}
	}
	*scroll = actualScroll

	var visible strings.Builder
	visibleWidth := 0
	for _, item := range clusters {
		if item.colStart < actualScroll {
			continue
		}
		width := item.colEnd - item.colStart
		if visibleWidth+width > maxWidth {
			break
		}
		visible.WriteString(item.text)
		visibleWidth += width
	}
	cursorOffset := cursorCol - actualScroll
	if cursorOffset < 0 {
		cursorOffset = 0
	}
	if cursorOffset > maxWidth {
		cursorOffset = maxWidth
	}
	return visible.String(), cursorOffset
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
