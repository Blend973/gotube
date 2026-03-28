package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/user/gotube/internal/preview"
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
	screen      tcell.Screen
	model       *Model
	preview     *preview.Manager
	previewRect preview.Rect
	quit        chan struct{}
	done        bool
	renderMu    sync.Mutex
}

const previewRefreshToken = "preview-refresh"

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
	prv, err := preview.NewManager(screen)
	if err != nil {
		return nil, err
	}

	app := &App{
		screen:  screen,
		model:   model,
		preview: prv,
		quit:    make(chan struct{}),
	}
	if app.preview != nil && app.preview.Supported() {
		app.preview.SetRefreshHook(func() {
			_ = app.screen.PostEvent(tcell.NewEventInterrupt(previewRefreshToken))
		})
	}

	return app, nil
}

func (a *App) Run() error {
	defer func() {
		a.screen.Fini()
	}()

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
			a.screen.Sync()
		case *tcell.EventInterrupt:
			if ev.Data() != previewRefreshToken {
				continue
			}
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
	if a.done {
		return
	}
	if a.preview != nil {
		a.preview.Clear()
		a.preview.Close()
	}
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
		a.moveResultSelection(-1)
	case tcell.KeyDown:
		a.moveResultSelection(1)
	case tcell.KeyEnter:
		if len(a.model.videos) > 0 {
			a.playVideoWithAutoplay()
		}
	case tcell.KeyCtrlC:
		a.doQuit()
	case tcell.KeyRune:
		switch ev.Rune() {
		case 'k':
			a.moveResultSelection(-1)
		case 'j':
			a.moveResultSelection(1)
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

func (a *App) moveResultSelection(delta int) {
	if len(a.model.videos) == 0 {
		return
	}

	next := wrapSelectionIndex(a.model.selected, delta, len(a.model.videos))
	if next < 0 {
		return
	}
	a.model.selected = next

	maxItems := resultListMaxItems(a.model.height)
	if a.model.selected < a.model.scroll {
		a.model.scroll = a.model.selected
	}
	if a.model.selected >= a.model.scroll+maxItems {
		a.model.scroll = a.model.selected - maxItems + 1
	}

	maxScroll := len(a.model.videos) - maxItems
	if maxScroll < 0 {
		maxScroll = 0
	}
	if a.model.scroll > maxScroll {
		a.model.scroll = maxScroll
	}
	if a.model.scroll < 0 {
		a.model.scroll = 0
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
	a.renderMu.Lock()
	defer a.renderMu.Unlock()

	a.screen.Clear()

	switch a.model.state {
	case stateSearch:
		if a.preview != nil {
			a.preview.Clear()
		}
		a.renderSearch()
	case stateLoading:
		if a.preview != nil {
			a.preview.Clear()
		}
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

	if (a.model.state == stateResults || a.model.state == stateFormats || a.model.state == stateHelp) && a.preview != nil && a.preview.Supported() {
		a.updatePreview()
	}
}

func (a *App) renderSearch() {
	w, h := a.model.width, a.model.height

	title := "🔍 GoTube - YouTube Terminal Viewer"
	titleX := (w - displayWidth(title)) / 2
	if titleX < 0 {
		titleX = 0
	}
	titleY := h/2 - 4
	a.drawText(titleX, titleY, title, tcell.Color(0x7c3aed), tcell.Color(0x0f0f1a), true)

	searchLabel := "Search: "
	searchInput := a.model.searchInput + "▏"
	searchBox := searchLabel + searchInput
	boxW := displayWidth(searchBox) + 4
	searchX := (w - boxW + 2) / 2
	if searchX < 0 {
		searchX = 0
	}
	searchY := h / 2

	boxX := searchX - 2
	if boxX < 0 {
		boxX = 0
	}
	if boxX+boxW > w {
		boxW = w - boxX
		if boxW < 1 {
			boxW = 1
		}
	}
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
	helpX := (w - displayWidth(help)) / 2
	if helpX < 0 {
		helpX = 0
	}
	helpY := h/2 + 4
	a.drawText(helpX, helpY, help, tcell.Color(0x71717a), tcell.Color(0x0f0f1a), false)
}

func (a *App) renderLoading() {
	w, h := a.model.width, a.model.height

	title := "🔍 GoTube"
	titleX := (w - displayWidth(title)) / 2
	if titleX < 0 {
		titleX = 0
	}
	titleY := h/2 - 2
	a.drawText(titleX, titleY, title, tcell.Color(0x7c3aed), tcell.Color(0x0f0f1a), true)

	spinner := "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	spinnerRunes := []rune(spinner)
	idx := int(time.Now().UnixNano()/100000000) % len(spinnerRunes)
	loading := string(spinnerRunes[idx]) + " Searching for: " + a.model.searchQuery + "..."
	loadingX := (w - displayWidth(loading)) / 2
	if loadingX < 0 {
		loadingX = 0
	}
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

	a.drawHeader(w)

	previewW := previewPaneWidth(w)

	previewRect := preview.Rect{}
	listX := 0
	listW := w
	if previewW > 0 {
		previewRect = preview.Rect{
			X: 0,
			Y: headerH,
			W: previewW,
			H: contentH,
		}
		listX = previewW + 1
		listW = w - listX
		for y := headerH; y < headerH+contentH; y++ {
			a.screen.SetContent(previewW, y, tcell.RuneVLine, nil, tcell.StyleDefault.
				Foreground(tcell.Color(0x232335)).
				Background(tcell.Color(0x1a1a2e)))
		}
	}

	a.previewRect = previewRect
	if previewW == 0 && a.preview != nil {
		a.preview.Clear()
	} else if previewW > 0 && (a.preview == nil || !a.preview.Supported()) {
		a.drawPreviewPlaceholder(previewRect)
	}

	a.drawVideoList(listX, headerH, listW, contentH)

	a.drawStatusBar(0, h-1, w)
}

func (a *App) drawHeader(w int) {
	header := "🔍 GoTube › " + truncateByWidth(a.model.searchQuery, maxInt(0, w-20))
	for x := 0; x < w; x++ {
		a.screen.SetContent(x, 0, ' ', nil, tcell.StyleDefault.
			Background(tcell.Color(0x0f0f1a)).
			Foreground(tcell.Color(0x7c3aed)))
	}
	a.drawText(0, 0, header, tcell.Color(0x7c3aed), tcell.Color(0x0f0f1a), true)

	autoplayStr := fmt.Sprintf("Autoplay: %s", a.model.autoplay.String())
	audioStr := ""
	if a.model.audioOnly {
		audioStr = " [Audio Only]"
	}
	statusStr := autoplayStr + audioStr

	for x := 0; x < w; x++ {
		a.screen.SetContent(x, 1, ' ', nil, tcell.StyleDefault.Background(tcell.Color(0x0f0f1a)))
	}
	statusX := w - displayWidth(statusStr) - 2
	if statusX < 0 {
		statusX = 0
	}
	a.drawText(statusX, 1, statusStr, tcell.Color(0x06b6d4), tcell.Color(0x0f0f1a), false)
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
		msgX := x + (w-displayWidth(msg))/2
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

		fgColor := tcell.Color(0xe4e4e7)
		if isSelected {
			fgColor = tcell.Color(0xffffff)
		}
		titleMaxW := maxInt(1, w-6)
		titleLines := wrapText(v.Title, titleMaxW)
		if len(titleLines) == 0 {
			titleLines = []string{""}
		}
		if len(titleLines) > 2 {
			titleLines = titleLines[:2]
		}
		a.drawText(x, y+row, prefix+truncateByWidth(titleLines[0], titleMaxW), fgColor, bgColor, isSelected)
		metaRow := row + 1
		if len(titleLines) > 1 && row+1 < h {
			indent := strings.Repeat(" ", displayWidth(prefix))
			secondMaxW := maxInt(1, w-displayWidth(indent))
			a.drawText(x, y+row+1, indent+truncateByWidth(titleLines[1], secondMaxW), fgColor, bgColor, isSelected)
			metaRow = row + 2
		}

		channel := truncateByWidth(v.Channel, 20)
		meta := fmt.Sprintf("   %s • %s • %s • %s", channel, v.Duration, FormatViews(v.Views), v.UploadDate)
		if metaRow < h {
			a.drawText(x, y+metaRow, truncateByWidth(meta, w), tcell.Color(0x06b6d4), bgColor, false)
		}

		row += itemH
	}
}

func (a *App) drawStatusBar(x, y, w int) {
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

	for col := 0; col < w; col++ {
		a.screen.SetContent(x+col, y, ' ', nil, tcell.StyleDefault.
			Background(tcell.Color(0x313244)))
	}

	cursor := x
	for i, k := range keys {
		if i > 0 {
			a.drawText(cursor, y, "  ", tcell.Color(0x71717a), tcell.Color(0x313244), false)
			cursor += 2
		}
		item := k.key + " " + k.desc
		a.drawText(cursor, y, item, tcell.Color(0x7c3aed), tcell.Color(0x313244), false)
		cursor += displayWidth(item)
		if cursor >= x+w {
			break
		}
	}
}

func (a *App) updatePreview() {
	if a.preview == nil || !a.preview.Supported() || len(a.model.videos) == 0 {
		return
	}
	if a.previewRect.W <= 0 || a.previewRect.H <= 0 {
		a.preview.Clear()
		return
	}

	selected := a.model.videos[a.model.selected]
	item := preview.Item{
		Key:          selected.ID,
		ThumbnailURL: selected.ThumbnailURL,
	}
	a.preview.Update(item, a.previewRect)
}

func (a *App) drawPreviewPlaceholder(rect preview.Rect) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}

	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			a.screen.SetContent(x, y, ' ', nil, tcell.StyleDefault.
				Background(tcell.Color(0x141421)))
		}
	}

	msg := "No image renderer"
	if rect.W >= displayWidth(msg) {
		textX := rect.X + (rect.W-displayWidth(msg))/2
		textY := rect.Y + rect.H/2
		a.drawText(textX, textY, msg, tcell.Color(0x71717a), tcell.Color(0x141421), false)
	}
}

func (a *App) renderFormats() {
	w, h := a.model.width, a.model.height

	boxX, boxY, boxW, boxH := dialogRect(w, h, 45, 14)
	a.drawPopupFrame(boxX, boxY, boxW, boxH)

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
		a.drawText(boxX+2, lineY, truncateByWidth(line, maxInt(1, boxW-4)), fg, bg, i == a.model.selectedFmt)
		lineY++
	}

	help := "Enter: Select  Esc: Cancel"
	a.drawText(boxX+2, boxY+boxH-2, help, tcell.Color(0x71717a), tcell.Color(0x1e1e2e), false)
}

func (a *App) renderHelp() {
	w, h := a.model.width, a.model.height

	boxX, boxY, boxW, boxH := dialogRect(w, h, 55, 16)
	a.drawPopupFrame(boxX, boxY, boxW, boxH)

	title := "Keyboard Shortcuts"
	a.drawText(boxX+2, boxY+1, title, tcell.Color(0x7c3aed), tcell.Color(0x1e1e2e), true)

	help := a.model.keymap.FullHelp()
	lineY := boxY + 3
	for _, row := range help {
		line := fmt.Sprintf(" %-12s %s", row[0], row[1])
		a.drawText(boxX+2, lineY, truncateByWidth(line, maxInt(1, boxW-4)), tcell.Color(0x7c3aed), tcell.Color(0x1e1e2e), false)
		lineY++
	}

	hint := "Press any key to close"
	a.drawText(boxX+2, boxY+boxH-2, hint, tcell.Color(0x71717a), tcell.Color(0x1e1e2e), false)
}

func (a *App) drawPopupFrame(boxX, boxY, boxW, boxH int) {
	if boxW < 1 || boxH < 1 {
		return
	}

	bg := tcell.Color(0x1e1e2e)
	fg := tcell.Color(0x7c3aed)
	for row := boxY; row < boxY+boxH; row++ {
		for col := boxX; col < boxX+boxW; col++ {
			style := tcell.StyleDefault.Background(bg)
			if row == boxY || row == boxY+boxH-1 || col == boxX || col == boxX+boxW-1 {
				style = style.Foreground(fg)
			}
			a.screen.SetContent(col, row, ' ', nil, style)
		}
	}

	if boxW >= 2 {
		for x := boxX + 1; x < boxX+boxW-1; x++ {
			a.screen.SetContent(x, boxY, tcell.RuneHLine, nil, tcell.StyleDefault.Foreground(fg).Background(bg))
			a.screen.SetContent(x, boxY+boxH-1, tcell.RuneHLine, nil, tcell.StyleDefault.Foreground(fg).Background(bg))
		}
	}
	if boxH >= 2 {
		for y := boxY + 1; y < boxY+boxH-1; y++ {
			a.screen.SetContent(boxX, y, tcell.RuneVLine, nil, tcell.StyleDefault.Foreground(fg).Background(bg))
			a.screen.SetContent(boxX+boxW-1, y, tcell.RuneVLine, nil, tcell.StyleDefault.Foreground(fg).Background(bg))
		}
	}

	a.screen.SetContent(boxX, boxY, tcell.RuneULCorner, nil, tcell.StyleDefault.Foreground(fg).Background(bg))
	a.screen.SetContent(boxX+boxW-1, boxY, tcell.RuneURCorner, nil, tcell.StyleDefault.Foreground(fg).Background(bg))
	a.screen.SetContent(boxX, boxY+boxH-1, tcell.RuneLLCorner, nil, tcell.StyleDefault.Foreground(fg).Background(bg))
	a.screen.SetContent(boxX+boxW-1, boxY+boxH-1, tcell.RuneLRCorner, nil, tcell.StyleDefault.Foreground(fg).Background(bg))
}

func dialogRect(totalW, totalH, maxW, maxH int) (boxX, boxY, boxW, boxH int) {
	if totalW < 1 {
		totalW = 1
	}
	if totalH < 1 {
		totalH = 1
	}

	contentX := 0
	contentW := totalW
	if previewW := previewPaneWidth(totalW); previewW > 0 {
		listX := previewW + 1
		if listX < totalW {
			contentX = listX
			contentW = totalW - contentX
		}
	}

	boxW = minInt(maxW, maxInt(1, contentW-4))
	if boxW > contentW {
		boxW = contentW
	}
	if boxW < 1 {
		boxW = 1
	}

	boxH = minInt(maxH, maxInt(1, totalH-4))
	if boxH > totalH {
		boxH = totalH
	}
	if boxH < 1 {
		boxH = 1
	}

	boxX = contentX + (contentW-boxW)/2
	if boxX < 0 {
		boxX = 0
	}
	if boxX+boxW > totalW {
		boxX = maxInt(0, totalW-boxW)
	}

	boxY = (totalH - boxH) / 2
	if boxY < 0 {
		boxY = 0
	}
	if boxY+boxH > totalH {
		boxY = maxInt(0, totalH-boxH)
	}

	return boxX, boxY, boxW, boxH
}

func (a *App) drawText(x, y int, text string, fg, bg tcell.Color, bold bool) {
	w, h := a.model.width, a.model.height
	if y < 0 || y >= h || x >= w {
		return
	}

	style := tcell.StyleDefault.Foreground(fg).Background(bg)
	if bold {
		style = style.Bold(true)
	}
	a.screen.PutStrStyled(x, y, text, style)
}

func (a *App) searchVideos() {
	result, err := a.model.scraper.Search(a.model.searchQuery, 1)
	if a.done {
		return
	}
	if err != nil {
		a.model.err = err
		a.model.state = stateSearch
	} else {
		a.model.videos = result.Videos
		a.model.selected = 0
		a.model.scroll = 0
		a.model.state = stateResults
		if a.preview != nil && a.preview.Supported() {
			items := make([]preview.Item, 0, len(a.model.videos))
			for _, v := range a.model.videos {
				items = append(items, preview.Item{
					Key:          v.ID,
					ThumbnailURL: v.ThumbnailURL,
				})
			}
			a.preview.Prefetch(items)
		}
	}
	if a.done {
		return
	}
	a.render()
}

func (a *App) playVideoWithAutoplay() {
	if a.preview != nil {
		a.preview.Clear()
	}
	a.screen.Fini()
	time.Sleep(100 * time.Millisecond)

	for {
		if len(a.model.videos) == 0 || a.model.selected >= len(a.model.videos) {
			break
		}

		v := a.model.videos[a.model.selected]

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
	if a.preview != nil {
		a.preview.RebindScreen(a.screen)
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

	if a.preview != nil {
		a.preview.Clear()
	}
	a.screen.Fini()
	time.Sleep(100 * time.Millisecond)

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
	if a.preview != nil {
		a.preview.RebindScreen(a.screen)
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
	if err := cmd.Start(); err != nil {
		a.model.err = err
		a.render()
		return
	}
	go func() {
		_ = cmd.Wait()
	}()
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
