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

## Installation

```bash
# Clone and build
git clone https://github.com/user/gotube
cd gotube
go build -o gotube ./cmd/gotube

# Or install to $GOPATH/bin
go install ./cmd/gotube
```

## Requirements

### Required
- `mpv` - Video player

### Optional
- `yt-dlp` or `youtube-dl` - For downloads and format selection

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
| `q` | Quit |

## Autoplay Modes

1. **Off**: Default mode. Plays a single video and returns to the list.

2. **Playlist**: After playing a video, automatically plays the next video in the search results. Great for watching multiple videos in sequence.

3. **Related**: After playing a video, fetches related videos from YouTube's "mix" and plays the next recommended video. Perfect for music discovery and exploring similar content.

## Architecture

```
gotube/
├── cmd/gotube/           # Entry point
├── internal/
│   ├── scraper/          # Native YouTube scraping
│   │   ├── types.go      # Video, Stream structs
│   │   └── youtube.go    # HTML parsing, ytInitialData extraction
│   └── tui/              # TUI
│       ├── app.go        # Main model + playback logic
│       ├── styles.go     # Helper functions
│       └── keybinds.go   # Key bindings
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

## License

MIT
