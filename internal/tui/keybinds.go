package tui

type KeyMap struct{}

func DefaultKeyMap() KeyMap {
	return KeyMap{}
}

func (k KeyMap) FullHelp() [][]string {
	return [][]string{
		{"↑/↓", "Navigate videos"},
		{"Enter", "Play selected video"},
		{"a", "Toggle autoplay (Off/Playlist/Related)"},
		{"m", "Toggle audio-only mode"},
		{"f", "Select video format/resolution"},
		{"d", "Download video"},
		{"/", "Start new search"},
		{"?", "Toggle help"},
		{"q", "Quit"},
	}
}
