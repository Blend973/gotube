package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/user/gotube/internal/config"
	"github.com/user/gotube/internal/tui"
)

var version = "1.0.0"

func main() {
	var configPath string
	args := os.Args[1:]

	// Parse --config flag before other flags so we can load config early.
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			configPath = args[i+1]
			// Remove flag and value so remaining loop doesn't trip on them.
			args = append(args[:i], args[i+2:]...)
			i--
		case strings.HasPrefix(args[i], "--config="):
			configPath = strings.TrimPrefix(args[i], "--config=")
			args = append(args[:i], args[i+1:]...)
			i--
		}
	}

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

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}

	app, err := tui.NewApp(cfg)
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
    -h, --help            Show this help message
    -v, --version         Show version information
    --config <path>       Path to config file (default: ~/.config/gotube/config.json)

KEYBINDINGS:
    ↑/↓              Navigate videos
    j/k              Navigate videos (vim-style)
    Enter            Play selected video
    a                Toggle autoplay (Off/Playlist/Related)
    m                Toggle audio-only mode
    f                Open resolution/format selector
    d                Download video using yt-dlp
    Ctrl+B            Delete word before cursor
    Ctrl+F            Delete word after cursor
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
