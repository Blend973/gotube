# gotube

A terminal YouTube viewer with native UI, autoplay support, and resolution selection.

## Features

- **Native TUI**: Built with tcell for smooth terminal interaction
- **Autoplay Modes**:
  - **Off**: Single video playback
  - **Playlist**: Automatically plays next video in search results
  - **Related**: Fetches and plays related videos (YouTube mix)
- **Audio-Only Mode**: Toggle audio-only playback for music/podcasts
- **Native YouTube Scraping**: No external tools required for search
- **Resolution Selector**: Choose video quality before playback
- **MPV OSD**: Full terminal OSD showing duration, progress, and playback info
- **Download Support**: Download videos via yt-dlp
- **Thumbnail Preview**: Display video thumbnails in a side pane with support for kitty, iTerm2, and ueberzugpp
- **Automatic Thumbnail Caching**: Thumbnails are cached locally for fast subsequent display
- **Prefetching**: Automatically prefetch thumbnails for visible videos
- **Image Renderer Detection**: Automatically detects terminal capabilities for optimal image display

## Installation

```bash
# Clone and build
git clone https://github.com/Blend973/gotube.git
cd gotube
go build -o gotube ./cmd/gotube
sudo mv gotube /usr/local/bin/gotube

# Or install to $GOPATH/bin
go install ./cmd/gotube
```

## Pre-built Binaries

Pre-built binaries for macOS (Intel and Apple Silicon) and Linux (amd64) are available in the repository root:
- `gotube-darwin-amd64`
- `gotube-darwin-arm64`
- `gotube-linux-amd64`

Download the appropriate binary, make it executable (`chmod +x`), and run it directly. You can also find these binaries in the [GitHub Releases](https://github.com/Blend973/gotube/releases) page.

## Requirements

### Required
- `mpv` - Video player

### Optional
- `yt-dlp` or `youtube-dl` - For downloads and format selection
- **Image preview dependencies** (choose one based on your terminal):
  - `kitty` terminal with `kitten` or `icat` command (kitty graphics protocol)
  - `iTerm2` with `imgcat` script (iterm2 inline image protocol)
  - `ueberzugpp` (fallback for X11/Linux terminals without native graphics support; e.g., alacritty, st)

## Usage

```bash
gotube              # Start interactive search
gotube -h           # Show help
gotube -v           # Show version
```

## Key Bindings

| Key | Action |
|-----|--------|
| `↑/↓` or `j/k` | Navigate videos |
| `Enter` | Play selected video |
| `a` | Toggle autoplay (Off → Playlist → Related → Off) |
| `m` | Toggle audio-only mode |
| `f` | Open resolution selector |
| `d` | Download video |
| `/` | Start new search |
| `?` | Toggle help |
| `q` or `Ctrl+C` | Quit |
| `Esc` | Return from resolution selector; any key closes help |

## Autoplay Modes

1. **Off**: Default mode. Plays a single video and returns to the list.

2. **Playlist**: After playing a video, automatically plays the next video in the search results. Great for watching multiple videos in sequence.

3. **Related**: After playing a video, fetches related videos from YouTube's "mix" and plays the next recommended video. Perfect for music discovery and exploring similar content.

## Image Preview

gotube can display video thumbnails in a side pane while browsing search results. The preview feature supports multiple terminal graphics protocols:

- **kitty graphics protocol**: Automatically detected when running in kitty terminal with `kitten` or `icat` installed.
- **iTerm2 inline images**: Requires `imgcat` script (usually pre‑installed with iTerm2).
- **ueberzugpp**: Fallback renderer for X11/Linux terminals without native graphics support (e.g., alacritty, st). X11 only. Must be installed separately.

The renderer is auto‑detected based on your terminal and available tools. You can override detection with the `IMAGE_RENDERER` environment variable:

```bash
export IMAGE_RENDERER=ueberzugpp  # Force ueberzugpp (or kitty, icat, imgcat, none)
```

Thumbnails are downloaded once and cached in `~/.cache/gotube/preview_images/` (Linux/macOS). The cache is cleaned automatically after 24 hours.

## Architecture

```
gotube/
├── cmd/gotube/           # Entry point
├── internal/
│   ├── preview/          # Thumbnail preview manager
│   │   ├── manager.go    # Renderer detection, caching, rendering
│   │   └── ueberzugpp.go # Ueberzugpp session management
│   ├── scraper/          # Native YouTube scraping
│   │   ├── types.go      # Video, Stream structs
│   │   └── youtube.go    # HTML parsing, ytInitialData extraction
│   └── tui/              # TUI
│       ├── app.go        # Main model + playback logic
│       ├── styles.go     # Helper functions
│       ├── keybinds.go   # Key bindings
│       ├── selection.go  # Selection wrapping
│       └── text.go       # Text wrapping and preview width calculation
```

## How It Works

### YouTube Scraping
- Fetches YouTube search results page
- Extracts `ytInitialData` JSON from HTML
- Parses video metadata (title, channel, duration, views)
- No API key required, no external tools

### Autoplay
- **Playlist mode**: Increments selection index after each video
- **Related mode**: Fetches YouTube mix URL (`/watch?v=ID&list=RDID`) and extracts related videos

### Thumbnail Preview
- Detects terminal capabilities via `IMAGE_RENDERER` environment variable or automatic detection
- Downloads thumbnail images from YouTube and caches them locally
- Renders thumbnails using kitty graphics protocol, iTerm2 inline images, or ueberzugpp
- Prefetches thumbnails for visible videos to improve responsiveness

## License

MIT
