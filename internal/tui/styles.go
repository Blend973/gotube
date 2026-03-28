package tui

import (
	"fmt"
)

func FormatViews(views int64) string {
	if views >= 1000000000 {
		return fmt.Sprintf("%.1fB", float64(views)/1000000000)
	}
	if views >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(views)/1000000)
	}
	if views >= 1000 {
		return fmt.Sprintf("%.1fK", float64(views)/1000)
	}
	return fmt.Sprintf("%d", views)
}
