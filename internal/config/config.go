package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// qualityPresets maps human-readable quality labels to yt-dlp format strings.
var qualityPresets = map[string]string{
	"2160p": "bestvideo[height<=2160]+bestaudio/best[height<=2160]/best",
	"1440p": "bestvideo[height<=1440]+bestaudio/best[height<=1440]/best",
	"1080p": "bestvideo[height<=1080]+bestaudio/best[height<=1080]/best",
	"720p":  "bestvideo[height<=720]+bestaudio/best[height<=720]/best",
	"480p":  "bestvideo[height<=480]+bestaudio/best[height<=480]/best",
	"360p":  "bestvideo[height<=360]+bestaudio/best[height<=360]/best",
	"audio": "bestaudio/best",
}

// Config holds all gotube configuration loaded from a JSON file.
type Config struct {
	// Default video quality. Can be a simple label ("1080p", "720p", etc.)
	// or a raw yt-dlp format string if it doesn't match any preset.
	Quality string `json:"quality"`

	MPV     MPVConfig     `json:"mpv"`
	Preview PreviewConfig `json:"preview"`
}

type MPVConfig struct {
	// Extra command-line options passed to mpv on every invocation.
	// Example: ["--volume=60", "--speed=1.25"]
	Options []string `json:"options"`
}

type PreviewConfig struct {
	// Renderer override. Valid values: "auto", "kitty", "sixel",
	// "ueberzugpp", "imgcat", "none". Empty string means auto-detect.
	Renderer string `json:"renderer"`

	// Enabled controls whether the thumbnail preview pane appears.
	// Default: true.
	Enabled bool `json:"enabled"`

	// CacheDir overrides the default thumbnail cache location.
	// Default: $XDG_CACHE_HOME/gotube/preview_images or
	// $HOME/.cache/gotube/preview_images.
	CacheDir string `json:"cache_dir"`

	// CacheMaxAge sets how long (in hours) cached thumbnails are kept
	// before being cleaned up. Default: 24. Set to 0 to disable cleanup.
	CacheMaxAge int `json:"cache_max_age"`

	// CellWidth / CellHeight are the assumed terminal cell pixel dimensions
	// used to size a sixel image from a cell-based preview rectangle. The
	// target pixel size for the preview is rect.W*CellWidth x
	// rect.H*CellHeight, so the encoded image never exceeds the pane.
	// Defaults: 0 = auto-detect from the terminal (fallback 8x16).
	CellWidth  int `json:"cell_width"`
	CellHeight int `json:"cell_height"`

	// SixelDither enables Floyd–Steinberg dithering when encoding sixel
	// images. Dithering smooths color banding on smooth gradients but costs
	// CPU on every encode. Default: false (faster). Enable only if you see
	// visible banding in previews.
	SixelDither bool `json:"sixel_dither"`
}

// Load reads the config from path. If path is empty it tries the default
// location ($XDG_CONFIG_HOME/gotube/config.json). If the file doesn't exist
// it creates the directory and writes a default config file automatically.
func Load(path string) (*Config, error) {
	cfg := Default()

	resolved := path
	if resolved == "" {
		resolved = defaultPath()
	}
	if resolved == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			// First run — write defaults to disk.
			if writeErr := Save(resolved, cfg); writeErr != nil {
				return nil, fmt.Errorf("creating default config: %w", writeErr)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", resolved, err)
	}

	// Normalise renderer value.
	cfg.Preview.Renderer = strings.ToLower(strings.TrimSpace(cfg.Preview.Renderer))

	return cfg, nil
}

// Save writes cfg as pretty-printed JSON to path, creating directories as needed.
func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}

	return nil
}

// ResolveQuality converts a human-readable quality label ("1080p", "720p", etc.)
// to a yt-dlp format string. If the label doesn't match a known preset it's
// returned as-is (treating it as a raw yt-dlp format string).
func ResolveQuality(label string) string {
	if q, ok := qualityPresets[strings.ToLower(label)]; ok {
		return q
	}
	return label
}

// Default returns a Config populated with sensible defaults.
func Default() *Config {
	return &Config{
		Quality: "1080p",
		MPV: MPVConfig{
			Options: []string{},
		},
		Preview: PreviewConfig{
			Renderer:    "auto",
			Enabled:     true,
			CacheDir:    "",
			CacheMaxAge: 24,
			CellWidth:   0,
			CellHeight:  0,
			SixelDither: false,
		},
	}
}

// defaultPath returns $XDG_CONFIG_HOME/gotube/config.json.
func defaultPath() string {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "gotube", "config.json")
}

// MarshalJSON pretty-prints the config for display/export.
func (c *Config) MarshalJSON() ([]byte, error) {
	type alias Config // avoid infinite recursion
	return json.MarshalIndent((*alias)(c), "", "  ")
}
