package main

import (
	"fmt"
	"os"

	"github.com/user/gotube/internal/tui"
)

var version = "1.0.0"

func main() {
	args := os.Args[1:]

	for _, arg := range args {
		switch arg {
		case "-v", "--version":
			fmt.Printf("gotube version %s\n", version)
			os.Exit(0)
		case "-h", "--help":
			printHelp()
			os.Exit(0)
		}
	}

	app, err := tui.NewApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	help := `gotube - A terminal YouTube viewer

USAGE:
    gotube [OPTIONS]

OPTIONS:
    -h, --help       Show this help message
    -v, --version    Show version information

KEYBINDINGS:
    ↑/↓              Navigate videos
    j/k              Navigate videos (vim-style)
    Enter            Play selected video
    a                Toggle autoplay (Off/Playlist/Related)
    m                Toggle audio-only mode
    f                Open resolution/format selector
    d                Download video using yt-dlp
    /                Start new search
    ?                Toggle help
    q/Esc            Quit

REQUIREMENTS:
    • mpv (for video playback)
    • yt-dlp or youtube-dl (optional, for downloads)

EXAMPLES:
    gotube                    Start interactive search
    gotube -v                 Show version

For more information, visit: https://github.com/user/gotube
`
	fmt.Println(help)
}
