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

func (s *YouTubeScraper) SearchFromMix(mixURL string) (*SearchResult, error) {
	req, err := http.NewRequest("GET", mixURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch mix: %w", err)
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

func (s *YouTubeScraper) parseVideoRenderer(vr *videoRenderer) Video {
	video := Video{
		ID:    vr.VideoID,
		Title: vr.Title.Runs[0].Text,
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

	return video
}

func extractYtInitialData(html string) (string, error) {
	re := regexp.MustCompile(`var\s+ytInitialData\s*=\s*(\{.+?\});\s*</script>`)
	matches := re.FindStringSubmatch(html)
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
	viewsStr = strings.ReplaceAll(viewsStr, ",", "")
	viewsStr = strings.ReplaceAll(viewsStr, " views", "")
	viewsStr = strings.ReplaceAll(viewsStr, "view", "")
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
}
