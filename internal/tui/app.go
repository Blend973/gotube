package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/user/gotube/internal/scraper"
)

type AutoplayMode int

const (
	AutoplayOff AutoplayMode = iota
	AutoplayPlaylist
	AutoplayRelated
)

func (a AutoplayMode) String() string {
	switch a {
	case AutoplayPlaylist:
		return "Playlist"
	case AutoplayRelated:
		return "Related"
	default:
		return "Off"
	}
}

func (a AutoplayMode) Toggle() AutoplayMode {
	switch a {
	case AutoplayOff:
		return AutoplayPlaylist
	case AutoplayPlaylist:
		return AutoplayRelated
	default:
		return AutoplayOff
	}
}

type App struct {
	screen tcell.Screen
	model  *Model
	quit   chan struct{}
	done   bool
}

type Model struct {
	state       state
	searchQuery string
	searchInput string
	videos      []scraper.Video
	selected    int
	scroll      int
	formats     []scraper.Stream
	selectedFmt int

	keymap  KeyMap
	scraper *scraper.YouTubeScraper

	autoplay  AutoplayMode
	audioOnly bool

	width  int
	height int
	err    error
}

type state int

const (
	stateSearch state = iota
	stateLoading
	stateResults
	stateFormats
	stateHelp
)

func NewApp() (*App, error) {
	screen, err := tcell.NewScreen()
	if err != nil {
		return nil, err
	}

	if err := screen.Init(); err != nil {
		return nil, err
	}

	model := NewModel()

	app := &App{
		screen: screen,
		model:  model,
		quit:   make(chan struct{}),
	}

	return app, nil
}

func (a *App) Run() error {
	defer a.screen.Fini()

	a.screen.SetStyle(tcell.StyleDefault.
		Background(tcell.Color(0x0f0f1a)).
		Foreground(tcell.Color(0xe4e4e7)))

	a.model.width, a.model.height = a.screen.Size()

	a.render()

	go a.handleEvents()

	<-a.quit
	return nil
}

func (a *App) handleEvents() {
	for {
		if a.done {
			return
		}
		ev := a.screen.PollEvent()
		if ev == nil {
			continue
		}

		switch ev := ev.(type) {
		case *tcell.EventKey:
			a.handleKey(ev)
		case *tcell.EventResize:
			a.model.width, a.model.height = a.screen.Size()
		}

		a.render()
	}
}

func (a *App) handleKey(ev *tcell.EventKey) {
	switch a.model.state {
	case stateSearch:
		a.handleSearchKey(ev)
	case stateResults:
		a.handleResultsKey(ev)
	case stateFormats:
		a.handleFormatsKey(ev)
	case stateHelp:
		a.model.state = stateResults
	}
}

func (a *App) doQuit() {
	a.screen.Clear()
	a.screen.Show()
	a.done = true
	close(a.quit)
}

func (a *App) handleSearchKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEnter:
		if a.model.searchInput != "" {
			a.model.searchQuery = a.model.searchInput
			a.model.state = stateLoading
			a.render()
			go a.searchVideos()
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(a.model.searchInput) > 0 {
			runes := []rune(a.model.searchInput)
			a.model.searchInput = string(runes[:len(runes)-1])
		}
	case tcell.KeyCtrlC:
		a.doQuit()
	case tcell.KeyRune:
		if ev.Rune() == 'q' {
			a.doQuit()
		} else {
			a.model.searchInput += string(ev.Rune())
		}
	}
}

func (a *App) handleResultsKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyUp:
		if a.model.selected > 0 {
			a.model.selected--
			if a.model.selected < a.model.scroll {
				a.model.scroll = a.model.selected
			}
		}
	case tcell.KeyDown:
		if a.model.selected < len(a.model.videos)-1 {
			a.model.selected++
			headerH := 2
			statusH := 1
			itemH := 3
			contentH := a.model.height - headerH - statusH
			maxItems := contentH / itemH
			if maxItems < 1 {
				maxItems = 1
			}
			if a.model.selected >= a.model.scroll+maxItems {
				a.model.scroll = a.model.selected - maxItems + 1
			}
		}
	case tcell.KeyEnter:
		if len(a.model.videos) > 0 {
			a.playVideoWithAutoplay()
		}
	case tcell.KeyCtrlC:
		a.doQuit()
	case tcell.KeyRune:
		switch ev.Rune() {
		case 'k':
			if a.model.selected > 0 {
				a.model.selected--
				if a.model.selected < a.model.scroll {
					a.model.scroll = a.model.selected
				}
			}
		case 'j':
			if a.model.selected < len(a.model.videos)-1 {
				a.model.selected++
				headerH := 2
				statusH := 1
				itemH := 3
				contentH := a.model.height - headerH - statusH
				maxItems := contentH / itemH
				if maxItems < 1 {
					maxItems = 1
				}
				if a.model.selected >= a.model.scroll+maxItems {
					a.model.scroll = a.model.selected - maxItems + 1
				}
			}
		case 'f':
			a.model.state = stateFormats
			a.model.selectedFmt = 0
			a.model.formats = getDefaultFormats()
		case 'd':
			if len(a.model.videos) > 0 {
				a.downloadVideo()
			}
		case 'a':
			a.model.autoplay = a.model.autoplay.Toggle()
		case 'm':
			a.model.audioOnly = !a.model.audioOnly
		case '/':
			a.model.state = stateSearch
			a.model.searchInput = ""
		case '?':
			a.model.state = stateHelp
		case 'q':
			a.doQuit()
		}
	}
}

func (a *App) handleFormatsKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyUp:
		if a.model.selectedFmt > 0 {
			a.model.selectedFmt--
		}
	case tcell.KeyDown:
		if a.model.selectedFmt < len(a.model.formats)-1 {
			a.model.selectedFmt++
		}
	case tcell.KeyEnter:
		if len(a.model.videos) > 0 && len(a.model.formats) > 0 {
			a.playVideoWithFormat()
		}
	case tcell.KeyEscape:
		a.model.state = stateResults
	case tcell.KeyRune:
		if ev.Rune() == 'q' {
			a.model.state = stateResults
		}
	}
}

func (a *App) render() {
	a.screen.Clear()

	switch a.model.state {
	case stateSearch:
		a.renderSearch()
	case stateLoading:
		a.renderLoading()
	case stateResults:
		a.renderResults()
	case stateFormats:
		a.renderResults()
		a.renderFormats()
	case stateHelp:
		a.renderResults()
		a.renderHelp()
	}

	a.screen.Show()
}

func (a *App) renderSearch() {
	w, h := a.model.width, a.model.height

	title := "🔍 GoTube - YouTube Terminal Viewer"
	titleRunes := []rune(title)
	titleX := (w - len(titleRunes)) / 2
	titleY := h/2 - 4
	a.drawText(titleX, titleY, title, tcell.Color(0x7c3aed), tcell.Color(0x0f0f1a), true)

	searchLabel := "Search: "
	searchInput := a.model.searchInput + "▏"
	searchBox := searchLabel + searchInput
	searchRunes := []rune(searchBox)
	boxW := len(searchRunes) + 4
	searchX := (w - boxW + 2) / 2
	searchY := h / 2

	boxX := searchX - 2
	for x := boxX; x < boxX+boxW; x++ {
		a.screen.SetContent(x, searchY-1, tcell.RuneHLine, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
		a.screen.SetContent(x, searchY+1, tcell.RuneHLine, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	}
	a.screen.SetContent(boxX, searchY-1, tcell.RuneULCorner, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	a.screen.SetContent(boxX+boxW-1, searchY-1, tcell.RuneURCorner, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	a.screen.SetContent(boxX, searchY+1, tcell.RuneLLCorner, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	a.screen.SetContent(boxX+boxW-1, searchY+1, tcell.RuneLRCorner, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	a.screen.SetContent(boxX, searchY, tcell.RuneVLine, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	a.screen.SetContent(boxX+boxW-1, searchY, tcell.RuneVLine, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))

	a.drawText(searchX, searchY, searchBox, tcell.Color(0xe4e4e7), tcell.Color(0x0f0f1a), false)

	help := "Press Enter to search • q to quit"
	helpRunes := []rune(help)
	helpX := (w - len(helpRunes)) / 2
	helpY := h/2 + 4
	a.drawText(helpX, helpY, help, tcell.Color(0x71717a), tcell.Color(0x0f0f1a), false)
}

func (a *App) renderLoading() {
	w, h := a.model.width, a.model.height

	title := "🔍 GoTube"
	titleRunes := []rune(title)
	titleX := (w - len(titleRunes)) / 2
	titleY := h/2 - 2
	a.drawText(titleX, titleY, title, tcell.Color(0x7c3aed), tcell.Color(0x0f0f1a), true)

	spinner := "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	spinnerRunes := []rune(spinner)
	idx := int(time.Now().UnixNano()/100000000) % len(spinnerRunes)
	loading := string(spinnerRunes[idx]) + " Searching for: " + a.model.searchQuery + "..."
	loadingRunes := []rune(loading)
	loadingX := (w - len(loadingRunes)) / 2
	loadingY := h / 2
	a.drawText(loadingX, loadingY, loading, tcell.Color(0x7c3aed), tcell.Color(0x0f0f1a), true)
}

func (a *App) renderResults() {
	w, h := a.model.width, a.model.height

	if w < 20 || h < 10 {
		return
	}

	headerH := 2
	statusH := 1
	contentH := h - headerH - statusH
	if contentH < 1 {
		contentH = 1
	}

	a.drawHeader(w, headerH)

	a.drawVideoList(0, headerH, w, contentH)

	a.drawStatusBar(0, h-1, w, statusH)
}

func (a *App) drawHeader(w, h int) {
	header := "🔍 GoTube › " + Truncate(a.model.searchQuery, w-20)
	runes := []rune(header)
	for x := 0; x < w; x++ {
		a.screen.SetContent(x, 0, ' ', nil, tcell.StyleDefault.
			Background(tcell.Color(0x0f0f1a)).
			Foreground(tcell.Color(0x7c3aed)))
		if x < len(runes) {
			a.screen.SetContent(x, 0, runes[x], nil, tcell.StyleDefault.
				Background(tcell.Color(0x0f0f1a)).
				Foreground(tcell.Color(0x7c3aed)).Bold(true))
		}
	}

	autoplayStr := fmt.Sprintf("Autoplay: %s", a.model.autoplay.String())
	audioStr := ""
	if a.model.audioOnly {
		audioStr = " [Audio Only]"
	}
	statusStr := autoplayStr + audioStr
	statusRunes := []rune(statusStr)

	for x := 0; x < w; x++ {
		a.screen.SetContent(x, 1, ' ', nil, tcell.StyleDefault.Background(tcell.Color(0x0f0f1a)))
	}
	a.drawText(w-len(statusRunes)-2, 1, statusStr, tcell.Color(0x06b6d4), tcell.Color(0x0f0f1a), false)
}

func (a *App) drawVideoList(x, y, w, h int) {
	if w < 1 || h < 1 {
		return
	}

	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			a.screen.SetContent(x+col, y+row, ' ', nil, tcell.StyleDefault.
				Background(tcell.Color(0x1a1a2e)))
		}
	}

	if len(a.model.videos) == 0 {
		msg := "No videos found"
		msgRunes := []rune(msg)
		msgX := x + (w-len(msgRunes))/2
		msgY := y + h/2
		if msgX < x {
			msgX = x
		}
		if msgY < y {
			msgY = y
		}
		if msgY >= y+h {
			msgY = y
		}
		a.drawText(msgX, msgY, msg, tcell.Color(0x71717a), tcell.Color(0x1a1a2e), false)
		return
	}

	itemH := 3
	maxItems := h / itemH
	if maxItems < 1 {
		maxItems = 1
	}

	start := a.model.scroll
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(a.model.videos) {
		end = len(a.model.videos)
	}

	row := 0
	for i := start; i < end && row < h; i++ {
		v := a.model.videos[i]
		isSelected := i == a.model.selected

		bgColor := tcell.Color(0x1a1a2e)
		if isSelected {
			bgColor = tcell.Color(0x313244)
		}

		for r := 0; r < itemH && row+r < h; r++ {
			for c := 0; c < w; c++ {
				a.screen.SetContent(x+c, y+row+r, ' ', nil, tcell.StyleDefault.Background(bgColor))
			}
		}

		prefix := "  "
		if isSelected {
			prefix = "▶ "
		}

		title := Truncate(v.Title, w-6)
		titleLine := prefix + title
		fgColor := tcell.Color(0xe4e4e7)
		if isSelected {
			fgColor = tcell.Color(0xffffff)
		}
		a.drawText(x, y+row, titleLine, fgColor, bgColor, isSelected)

		channel := Truncate(v.Channel, 20)
		meta := fmt.Sprintf("   %s • %s • %s • %s", channel, v.Duration, FormatViews(v.Views), v.UploadDate)
		if row+1 < h {
			a.drawText(x, y+row+1, meta, tcell.Color(0x06b6d4), bgColor, false)
		}

		row += itemH
	}
}

func (a *App) drawStatusBar(x, y, w, h int) {
	keys := []struct {
		key, desc string
	}{
		{"↑/↓", "Navigate"},
		{"Enter", "Play"},
		{"a", "Autoplay"},
		{"m", "Audio"},
		{"f", "Formats"},
		{"d", "Download"},
		{"/", "Search"},
		{"?", "Help"},
		{"q", "Quit"},
	}

	var items []string
	for _, k := range keys {
		items = append(items, k.key+" "+k.desc)
	}
	status := strings.Join(items, "  ")
	statusRunes := []rune(status)

	for col := 0; col < w; col++ {
		a.screen.SetContent(x+col, y, ' ', nil, tcell.StyleDefault.
			Background(tcell.Color(0x313244)))
		if col < len(statusRunes) {
			r := statusRunes[col]
			color := tcell.Color(0x7c3aed)
			if r == ' ' {
				color = tcell.Color(0x71717a)
			}
			a.screen.SetContent(x+col, y, r, nil, tcell.StyleDefault.
				Background(tcell.Color(0x313244)).
				Foreground(color))
		}
	}
}

func (a *App) renderFormats() {
	w, h := a.model.width, a.model.height

	boxW := 45
	boxH := 14
	boxX := (w - boxW) / 2
	boxY := (h - boxH) / 2

	for row := boxY; row < boxY+boxH; row++ {
		for col := boxX; col < boxX+boxW; col++ {
			style := tcell.StyleDefault.Background(tcell.Color(0x1e1e2e))
			if row == boxY || row == boxY+boxH-1 || col == boxX || col == boxX+boxW-1 {
				style = style.Foreground(tcell.Color(0x7c3aed))
			}
			a.screen.SetContent(col, row, ' ', nil, style)
		}
	}

	a.screen.SetContent(boxX, boxY, tcell.RuneULCorner, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	a.screen.SetContent(boxX+boxW-1, boxY, tcell.RuneURCorner, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	a.screen.SetContent(boxX, boxY+boxH-1, tcell.RuneLLCorner, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))
	a.screen.SetContent(boxX+boxW-1, boxY+boxH-1, tcell.RuneLRCorner, nil, tcell.StyleDefault.Foreground(tcell.Color(0x7c3aed)))

	title := "Select Resolution"
	a.drawText(boxX+2, boxY+1, title, tcell.Color(0x7c3aed), tcell.Color(0x1e1e2e), true)

	formats := []struct {
		quality, typ string
	}{
		{"1080p", "video+audio"},
		{"720p", "video+audio"},
		{"480p", "video+audio"},
		{"360p", "video+audio"},
		{"Audio", "audio only"},
	}

	lineY := boxY + 3
	for i, f := range formats {
		mark := "○"
		if i == a.model.selectedFmt {
			mark = "◉"
		}
		line := fmt.Sprintf(" %s %-8s %s", mark, f.quality, f.typ)
		fg := tcell.Color(0xe4e4e7)
		bg := tcell.Color(0x1e1e2e)
		if i == a.model.selectedFmt {
			bg = tcell.Color(0x313244)
			fg = tcell.Color(0xffffff)
		}
		a.drawText(boxX+2, lineY, line, fg, bg, i == a.model.selectedFmt)
		lineY++
	}

	help := "Enter: Select  Esc: Cancel"
	a.drawText(boxX+2, boxY+boxH-2, help, tcell.Color(0x71717a), tcell.Color(0x1e1e2e), false)
}

func (a *App) renderHelp() {
	w, h := a.model.width, a.model.height

	boxW := 55
	boxH := 16
	boxX := (w - boxW) / 2
	boxY := (h - boxH) / 2

	for row := boxY; row < boxY+boxH; row++ {
		for col := boxX; col < boxX+boxW; col++ {
			style := tcell.StyleDefault.Background(tcell.Color(0x1e1e2e))
			if row == boxY || row == boxY+boxH-1 || col == boxX || col == boxX+boxW-1 {
				style = style.Foreground(tcell.Color(0x7c3aed))
			}
			a.screen.SetContent(col, row, ' ', nil, style)
		}
	}

	title := "Keyboard Shortcuts"
	a.drawText(boxX+2, boxY+1, title, tcell.Color(0x7c3aed), tcell.Color(0x1e1e2e), true)

	help := a.model.keymap.FullHelp()
	lineY := boxY + 3
	for _, row := range help {
		line := fmt.Sprintf(" %-12s %s", row[0], row[1])
		a.drawText(boxX+2, lineY, line, tcell.Color(0x7c3aed), tcell.Color(0x1e1e2e), false)
		lineY++
	}

	hint := "Press any key to close"
	a.drawText(boxX+2, boxY+boxH-2, hint, tcell.Color(0x71717a), tcell.Color(0x1e1e2e), false)
}

func (a *App) drawText(x, y int, text string, fg, bg tcell.Color, bold bool) {
	w, _ := a.model.width, a.model.height
	runes := []rune(text)
	for i, r := range runes {
		if x+i >= w {
			break
		}
		if x+i < 0 {
			continue
		}
		style := tcell.StyleDefault.Foreground(fg).Background(bg)
		if bold {
			style = style.Bold(true)
		}
		a.screen.SetContent(x+i, y, r, nil, style)
	}
}

func (a *App) searchVideos() {
	result, err := a.model.scraper.Search(a.model.searchQuery, 1)
	if err != nil {
		a.model.err = err
		a.model.state = stateSearch
	} else {
		a.model.videos = result.Videos
		a.model.selected = 0
		a.model.scroll = 0
		a.model.state = stateResults
	}
	a.render()
}

func (a *App) playVideoWithAutoplay() {
	a.screen.Fini()
	time.Sleep(100 * time.Millisecond)

	for {
		if len(a.model.videos) == 0 || a.model.selected >= len(a.model.videos) {
			break
		}

		v := a.model.videos[a.model.selected]

		fmt.Printf("\033[2J\033[H")
		fmt.Printf("▶ Now Playing: %s\n", v.Title)
		if a.model.audioOnly {
			fmt.Printf("[Audio Only Mode]\n")
		}
		fmt.Printf("\n")

		args := []string{
			"--term-osd=force",
			"--term-osd-bar",
			"--force-window=no",
		}
		if a.model.audioOnly {
			args = append(args, "--no-video")
		}
		args = append(args, v.URL)

		cmd := exec.Command("mpv", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		startTime := time.Now()
		cmd.Run()
		elapsed := time.Since(startTime)

		if a.model.autoplay == AutoplayOff {
			break
		}

		if elapsed < 5*time.Second {
			break
		}

		if a.model.autoplay == AutoplayPlaylist {
			a.model.selected++
			if a.model.selected >= len(a.model.videos) {
				break
			}
		} else if a.model.autoplay == AutoplayRelated {
			relatedVideo, err := a.fetchRelatedVideo(v.ID)
			if err != nil {
				break
			}
			a.model.videos = append([]scraper.Video{*relatedVideo}, a.model.videos...)
			a.model.selected = 0
		}

		fmt.Printf("\033[2J\033[H")
		fmt.Printf("Next: %s\n", a.model.videos[a.model.selected].Title)
		time.Sleep(1 * time.Second)
	}

	time.Sleep(100 * time.Millisecond)

	var initErr error
	a.screen, initErr = tcell.NewScreen()
	if initErr != nil {
		a.doQuit()
		return
	}
	if err := a.screen.Init(); err != nil {
		a.doQuit()
		return
	}
	a.screen.SetStyle(tcell.StyleDefault.
		Background(tcell.Color(0x0f0f1a)).
		Foreground(tcell.Color(0xe4e4e7)))
	a.render()
}

func (a *App) fetchRelatedVideo(videoID string) (*scraper.Video, error) {
	mixURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s&list=RD%s", videoID, videoID)

	result, err := a.model.scraper.SearchFromMix(mixURL)
	if err != nil {
		return nil, err
	}

	if len(result.Videos) == 0 {
		return nil, fmt.Errorf("no related videos found")
	}

	for _, v := range result.Videos {
		if v.ID != videoID {
			return &v, nil
		}
	}

	return nil, fmt.Errorf("no related videos found")
}

func (a *App) playVideoWithFormat() {
	if len(a.model.videos) == 0 || len(a.model.formats) == 0 {
		return
	}
	v := a.model.videos[a.model.selected]
	f := a.model.formats[a.model.selectedFmt]

	a.screen.Fini()
	time.Sleep(100 * time.Millisecond)

	fmt.Printf("\033[2J\033[H")
	fmt.Printf("▶ Now Playing: %s\n", v.Title)
	fmt.Printf("Quality: %s\n\n", f.Quality)

	args := []string{
		"--term-osd=force",
		"--term-osd-bar",
		"--force-window=no",
		"--ytdl-format=" + f.Quality,
		v.URL,
	}

	cmd := exec.Command("mpv", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	time.Sleep(100 * time.Millisecond)

	var err error
	a.screen, err = tcell.NewScreen()
	if err != nil {
		a.doQuit()
		return
	}
	if err := a.screen.Init(); err != nil {
		a.doQuit()
		return
	}
	a.screen.SetStyle(tcell.StyleDefault.
		Background(tcell.Color(0x0f0f1a)).
		Foreground(tcell.Color(0xe4e4e7)))
	a.model.state = stateResults
	a.render()
}

func (a *App) downloadVideo() {
	if len(a.model.videos) == 0 {
		return
	}
	v := a.model.videos[a.model.selected]
	cmd := exec.Command("yt-dlp", v.URL)
	cmd.Start()
}

func NewModel() *Model {
	s := scraper.NewYouTubeScraper()

	return &Model{
		state:    stateSearch,
		keymap:   DefaultKeyMap(),
		scraper:  s,
		autoplay: AutoplayOff,
	}
}

func getDefaultFormats() []scraper.Stream {
	return []scraper.Stream{
		{Quality: "bestvideo[height<=1080]+bestaudio/best[height<=1080]/best"},
		{Quality: "bestvideo[height<=720]+bestaudio/best[height<=720]/best"},
		{Quality: "bestvideo[height<=480]+bestaudio/best[height<=480]/best"},
		{Quality: "bestvideo[height<=360]+bestaudio/best[height<=360]/best"},
		{Quality: "bestaudio/best"},
	}
}
