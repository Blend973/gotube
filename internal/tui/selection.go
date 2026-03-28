package tui

func wrapSelectionIndex(current, delta, size int) int {
	if size <= 0 {
		return -1
	}

	next := current + delta
	if next < 0 {
		return size - 1
	}
	if next >= size {
		return 0
	}
	return next
}

func resultListMaxItems(height int) int {
	headerH := 2
	statusH := 1
	itemH := 3
	contentH := height - headerH - statusH
	if contentH < 1 {
		contentH = 1
	}
	maxItems := contentH / itemH
	if maxItems < 1 {
		maxItems = 1
	}
	return maxItems
}
