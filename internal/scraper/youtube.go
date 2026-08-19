package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ytInitialDataRe = regexp.MustCompile(`var\s+ytInitialData\s*=\s*(\{.+?\});\s*</script>`)
	viewsReplacer   = strings.NewReplacer(",", "", " views", "", "view", "")
)

const (
	UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	BaseURL   = "https://www.youtube.com"
)

type YouTubeScraper struct {
	client *http.Client
}

func NewYouTubeScraper() *YouTubeScraper {
	return &YouTubeScraper{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    10,
				IdleConnTimeout: 60 * time.Second,
			},
		},
	}
}

func (s *YouTubeScraper) Search(query string, page int) (*SearchResult, error) {
	searchURL := fmt.Sprintf("%s/results?search_query=%s", BaseURL, url.QueryEscape(query))

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch search results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return s.parseSearchResults(string(body))
}

func (s *YouTubeScraper) parseSearchResults(html string) (*SearchResult, error) {
	result := &SearchResult{
		Videos: []Video{},
	}

	jsonData, err := extractYtInitialData(html)
	if err != nil {
		return nil, err
	}

	var ytData ytInitialData
	if err := json.Unmarshal([]byte(jsonData), &ytData); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	for _, content := range ytData.Contents.TwoColumnSearchResultsRenderer.PrimaryContents.SectionListRenderer.Contents {
		for _, item := range content.ItemSectionRenderer.Contents {
			if item.VideoRenderer.VideoID != "" {
				video := s.parseVideoRenderer(&item.VideoRenderer)
				result.Videos = append(result.Videos, video)
			}
		}
	}

	result.TotalResults = len(result.Videos)
	return result, nil
}

// GetRelatedVideos uses YouTube's InnerTube API to fetch algorithmically
// related videos — the same data YouTube's own player uses for "Up next".
func (s *YouTubeScraper) GetRelatedVideos(videoID string, limit int) ([]Video, error) {
	if limit <= 0 {
		limit = 20
	}

	requestBody := map[string]any{
		"videoId": videoID,
		"context": map[string]any{
			"client": map[string]any{
				"hl":            "en",
				"gl":            "US",
				"clientName":    "WEB",
				"clientVersion": "2.20241126.01.00",
			},
		},
	}

	data, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	apiURL := BaseURL + "/youtubei/v1/next?prettyPrint=false"
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch related videos: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return s.parseInnerTubeNext(body, videoID, limit)
}

func (s *YouTubeScraper) parseInnerTubeNext(body []byte, currentID string, limit int) ([]Video, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("failed to parse InnerTube response: %w", err)
	}

	rendererKeys := map[string]struct{}{
		"endScreenVideoRenderer":        {},
		"compactVideoRenderer":          {},
		"videoRenderer":                 {},
		"playerOverlayAutoplayRenderer": {},
	}

	results := make([]Video, 0, limit)
	seen := make(map[string]struct{}, limit)
	walkRenderers(root, rendererKeys, func(renderer map[string]any) {
		if len(results) >= limit {
			return
		}
		v := parseGenericVideoRenderer(renderer)
		if v.ID == "" || v.ID == currentID {
			return
		}
		if _, ok := seen[v.ID]; ok {
			return
		}
		seen[v.ID] = struct{}{}
		results = append(results, v)
	})

	return results, nil
}

// walkRenderers recursively walks a JSON tree looking for keys in rendererKeys.
func walkRenderers(node any, rendererKeys map[string]struct{}, emit func(map[string]any)) {
	switch typed := node.(type) {
	case map[string]any:
		for key, v := range typed {
			if _, ok := rendererKeys[key]; ok {
				if renderer, ok := v.(map[string]any); ok {
					emit(renderer)
				}
			}
			walkRenderers(v, rendererKeys, emit)
		}
	case []any:
		for _, v := range typed {
			walkRenderers(v, rendererKeys, emit)
		}
	}
}

// parseGenericVideoRenderer extracts a Video from an untyped JSON renderer map.
func parseGenericVideoRenderer(renderer map[string]any) Video {
	videoID, _ := renderer["videoId"].(string)
	title := readTextField(renderer["title"])
	channel := readTextField(renderer["ownerText"])
	if channel == "" {
		channel = readTextField(renderer["shortBylineText"])
	}
	duration := readTextField(renderer["lengthText"])
	views := readTextField(renderer["viewCountText"])
	if views == "" {
		views = readTextField(renderer["shortViewCountText"])
	}
	uploadDate := readTextField(renderer["publishedTimeText"])

	if channel == "" {
		channel = "Unknown"
	}

	var viewsInt int64
	if views != "" {
		viewsInt = parseViews(views)
	}

	var durationSec int
	if duration != "" {
		durationSec = parseDuration(duration)
	}

	return Video{
		ID:          videoID,
		Title:       title,
		Channel:     channel,
		Duration:    duration,
		DurationSec: durationSec,
		Views:       viewsInt,
		UploadDate:  uploadDate,
		URL:         fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID),
	}
}

// readTextField extracts text from a YouTube JSON text node (simpleText or runs).
func readTextField(node any) string {
	m, ok := node.(map[string]any)
	if !ok {
		return ""
	}
	if simple, ok := m["simpleText"].(string); ok {
		return strings.TrimSpace(simple)
	}
	runs, ok := m["runs"].([]any)
	if !ok || len(runs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(runs))
	for _, item := range runs {
		rm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, _ := rm["text"].(string)
		text = strings.TrimSpace(text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func (s *YouTubeScraper) parseVideoRenderer(vr *videoRenderer) Video {
	video := Video{
		ID:    vr.VideoID,
		Title: firstText(vr.Title.Runs, vr.Title.SimpleText),
		URL:   fmt.Sprintf("https://www.youtube.com/watch?v=%s", vr.VideoID),
	}

	if len(vr.OwnerText.Runs) > 0 {
		video.Channel = vr.OwnerText.Runs[0].Text
	}

	if len(vr.LengthText.Runs) > 0 {
		video.Duration = vr.LengthText.Runs[0].Text
		video.DurationSec = parseDuration(vr.LengthText.Runs[0].Text)
	} else if vr.LengthText.SimpleText != "" {
		video.Duration = vr.LengthText.SimpleText
		video.DurationSec = parseDuration(vr.LengthText.SimpleText)
	}

	if len(vr.ViewCountText.Runs) > 0 {
		video.Views = parseViews(vr.ViewCountText.Runs[0].Text)
	} else if vr.ViewCountText.SimpleText != "" {
		video.Views = parseViews(vr.ViewCountText.SimpleText)
	}

	if len(vr.PublishedTimeText.Runs) > 0 {
		video.UploadDate = vr.PublishedTimeText.Runs[0].Text
	} else if vr.PublishedTimeText.SimpleText != "" {
		video.UploadDate = vr.PublishedTimeText.SimpleText
	}

	if len(vr.Thumbnail.Thumbnails) > 0 {
		video.ThumbnailURL = vr.Thumbnail.Thumbnails[len(vr.Thumbnail.Thumbnails)-1].URL
	}
	if video.ThumbnailURL == "" && video.ID != "" {
		video.ThumbnailURL = fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", video.ID)
	}

	return video
}

func extractYtInitialData(html string) (string, error) {
	matches := ytInitialDataRe.FindStringSubmatch(html)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not find ytInitialData in HTML")
	}
	return matches[1], nil
}

func parseDuration(duration string) int {
	parts := strings.Split(duration, ":")
	var seconds int
	switch len(parts) {
	case 2:
		mins, _ := strconv.Atoi(parts[0])
		secs, _ := strconv.Atoi(parts[1])
		seconds = mins*60 + secs
	case 3:
		hours, _ := strconv.Atoi(parts[0])
		mins, _ := strconv.Atoi(parts[1])
		secs, _ := strconv.Atoi(parts[2])
		seconds = hours*3600 + mins*60 + secs
	}
	return seconds
}

func parseViews(viewsStr string) int64 {
	viewsStr = viewsReplacer.Replace(viewsStr)
	viewsStr = strings.TrimSpace(viewsStr)
	views, _ := strconv.ParseInt(viewsStr, 10, 64)
	return views
}

type ytInitialData struct {
	Contents struct {
		TwoColumnSearchResultsRenderer struct {
			PrimaryContents struct {
				SectionListRenderer struct {
					Contents []struct {
						ItemSectionRenderer struct {
							Contents []struct {
								VideoRenderer videoRenderer `json:"videoRenderer"`
							} `json:"contents"`
						} `json:"itemSectionRenderer"`
					} `json:"contents"`
				} `json:"sectionListRenderer"`
			} `json:"primaryContents"`
		} `json:"twoColumnSearchResultsRenderer"`
	} `json:"contents"`
}

type videoRenderer struct {
	VideoID string `json:"videoId"`
	Title   struct {
		Runs []struct {
			Text string `json:"text"`
		} `json:"runs"`
		SimpleText string `json:"simpleText"`
	} `json:"title"`
	OwnerText struct {
		Runs []struct {
			Text string `json:"text"`
		} `json:"runs"`
	} `json:"ownerText"`
	LengthText struct {
		Runs []struct {
			Text string `json:"text"`
		} `json:"runs"`
		SimpleText string `json:"simpleText"`
	} `json:"lengthText"`
	ViewCountText struct {
		Runs []struct {
			Text string `json:"text"`
		} `json:"runs"`
		SimpleText string `json:"simpleText"`
	} `json:"viewCountText"`
	PublishedTimeText struct {
		Runs []struct {
			Text string `json:"text"`
		} `json:"runs"`
		SimpleText string `json:"simpleText"`
	} `json:"publishedTimeText"`
	Thumbnail struct {
		Thumbnails []struct {
			URL string `json:"url"`
		} `json:"thumbnails"`
	} `json:"thumbnail"`
}

func firstText(runs []struct {
	Text string `json:"text"`
}, simpleText string) string {
	if len(runs) > 0 {
		return runs[0].Text
	}
	return simpleText
}
