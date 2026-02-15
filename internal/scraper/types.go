package scraper

type Video struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Channel     string `json:"channel"`
	Duration    string `json:"duration"`
	DurationSec int    `json:"duration_sec"`
	Views       int64  `json:"views"`
	UploadDate  string `json:"upload_date"`
	URL         string `json:"url"`
}

type Stream struct {
	Quality string `json:"quality"`
}

type SearchResult struct {
	Videos       []Video `json:"videos"`
	TotalResults int     `json:"total_results"`
}
