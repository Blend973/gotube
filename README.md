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
- **Thumbnail Preview**: Display video thumbnails in a side pane with support for kitty, iTerm2, ueberzugpp, and sixel
- **Automatic Thumbnail Caching**: Thumbnails are cached locally for fast subsequent display
- **Prefetching**: Automatically prefetch thumbnails for visible videos
- **Image Renderer Detection**: Automatically detects terminal capabilities for optimal image display
- **Fast Sixel Rendering**: A shared warm palette, an in-memory payload memo, and a pipelined decode/encode path keep sixel previews snappy while scrolling
- **Sixel Cell-Size Detection**: Auto-detects terminal cell pixel dimensions (`CSI 16 t`) so sixel images land exactly inside the preview pane
- **Configuration**: JSON config file with sensible defaults, auto-created on first run (`~/.config/gotube/config.json`)

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
gotube                          # Start interactive search
gotube -h                       # Show help
gotube -v                       # Show version
gotube --config /path/to/cfg    # Use a custom config file
```

## Configuration

gotube can be configured via a JSON config file. On first run, a default config is automatically created at `~/.config/gotube/config.json`. You can also pass a custom path with `--config`.

```json
{
  "quality": "1080p",
  "mpv": {
    "options": ["--volume=60", "--speed=1.25"]
  },
  "preview": {
    "renderer": "auto",
    "enabled": true,
    "cache_dir": "",
    "cache_max_age": 24,
    "cell_width": 0,
    "cell_height": 0,
    "sixel_dither": false
  }
}
```

### Options

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `quality` | string | `"1080p"` | Default video quality. Presets: `2160p`, `1440p`, `1080p`, `720p`, `480p`, `360p`, `audio`. Any other value is passed through as a raw yt-dlp format string. |
| `mpv.options` | array | `[]` | Extra command-line flags passed to mpv on every playback (e.g. `["--volume=50"]`). |
| `preview.renderer` | string | `"auto"` | Force an image renderer: `auto`, `kitty`, `sixel`, `imgcat`/`iterm2`, `ueberzugpp`, `none`. When `auto`, the renderer is detected from your terminal and the `IMAGE_RENDERER` env var. |
| `preview.enabled` | bool | `true` | Show/hide the thumbnail preview pane. |
| `preview.cache_dir` | string | `""` | Override the thumbnail cache location. Empty uses `$XDG_CACHE_HOME/gotube/preview_images`. |
| `preview.cache_max_age` | int | `24` | Hours before cached thumbnails are cleaned up. Set to `0` to disable cleanup. |
| `preview.cell_width` | int | `0` | Terminal cell width in pixels used to size a sixel image (`0` = auto-detect from the terminal, defaulting to `8`). |
| `preview.cell_height` | int | `0` | Terminal cell height in pixels used to size a sixel image (`0` = auto-detect from the terminal, defaulting to `16`). |
| `preview.sixel_dither` | bool | `false` | Enable Floyd–Steinberg dithering when encoding sixel images. Smoother gradients at a CPU cost; enable only if you see banding. |

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
- **sixel graphics**: Native sixel for terminals such as foot, mlterm, wezterm, contour, xterm (with sixel enabled), and Windows Terminal. Auto‑detected from `TERM`, or force with `preview.renderer = "sixel"`.
- **ueberzugpp**: Fallback renderer for X11/Linux terminals without native graphics support (e.g., alacritty, st). X11 only. Must be installed separately.

The renderer is auto‑detected based on your terminal and available tools. You can override detection via the config file or the `IMAGE_RENDERER` environment variable:

```json
{ "preview": { "renderer": "kitty" } }
```

```bash
export IMAGE_RENDERER=sixel  # env var fallback (or kitty, icat, imgcat, iterm2, ueberzugpp, none)
```

Thumbnails are downloaded once and cached in `~/.cache/gotube/preview_images/` (Linux/macOS) by default. The cache location and max age can be overridden in the config file. Cleanup runs automatically on startup.

#### Sixel performance

The sixel renderer reuses a single shared color palette (seeded once and cached by go-sixel) instead of re‑quantizing every image, so encodes after the first are roughly an order of magnitude faster. Finished escape sequences are memoized in memory by thumbnail and pane size, making revisits a bare terminal write rather than a re‑decode/re‑encode. Decoding the next image overlaps the current image's encoding on a bounded background pipeline, cutting wall time without adding CPU work.

## Architecture

```
gotube/
├── cmd/gotube/           # Entry point
├── internal/
│   ├── config/           # Configuration loading and defaults
│   │   └── config.go     # XDG paths, JSON parsing, quality presets
│   ├── preview/          # Thumbnail preview manager
│   │   ├── manager.go    # Renderer detection, caching, rendering
│   │   ├── image.go      # Image decode, WebP sniffing, resize
│   │   ├── iterm.go      # iTerm2 renderer (async)
│   │   ├── sixel.go      # Sixel renderer, palette + payload caching
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
- Detects terminal capabilities via config file (`preview.renderer`), `IMAGE_RENDERER` environment variable, or automatic detection
- Downloads thumbnail images from YouTube and caches them locally (configurable cache dir and max age)
- Renders thumbnails using kitty graphics protocol, iTerm2 inline images, ueberzugpp, or sixel
- Prefetches thumbnails for visible videos to improve responsiveness
- Sixel renderer detects the terminal cell pixel size (`CSI 16 t`) so images fit the pane exactly, keeps a shared warm palette, and memoizes finished payloads so repeated previews are served from memory instead of re-encoded

## License

MIT
