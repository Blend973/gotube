package tui

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/user/gotube/internal/config"
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
	cfg         *config.Config
	quit        chan struct{}
	quitOnce    sync.Once
	done        bool
	renderMu    sync.Mutex
	prevState   state
	prevWidth   int
	prevHeight  int
}

const previewRefreshToken = "preview-refresh"

type Model struct {
	state        state
	searchQuery  string
	searchInput  []rune
	searchPos    int
	searchScroll int
	videos       []scraper.Video
	selected     int
	scroll       int
	formats      []scraper.Stream
	selectedFmt  int

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

func NewApp(cfg *config.Config) (*App, error) {
	// If sixel will be the preview renderer, learn the real terminal cell
	// size before the TUI takes over the terminal. Must happen before
	// screen.Init(): once tcell's input loop is reading the TTY it would steal
	// the "CSI 16 t" response. Config cell_width/cell_height override this.
	var cellW, cellH int
	if preview.UsesSixel(&cfg.Preview) && (cfg.Preview.CellWidth <= 0 || cfg.Preview.CellHeight <= 0) {
		cellW, cellH = preview.QueryCellSize()
	}

	screen, err := tcell.NewScreen()
	if err != nil {
		return nil, err
	}

	if err := screen.Init(); err != nil {
		return nil, err
	}

	model := NewModel(cfg)
	prv, err := preview.NewManager(screen, &cfg.Preview, cellW, cellH)
	if err != nil {
		return nil, err
	}

	app := &App{
		screen:  screen,
		model:   model,
		preview: prv,
		cfg:     cfg,
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

	// Defensive quit: SIGINT/SIGTERM tears the app down even if the event
	// loop is wedged inside a render (issue: defensive checks in event loop).
	// doQuit closes a.quit, which unblocks the receive below.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			a.doQuit()
		case <-a.quit:
		}
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
			// Screen finalized (or the tty went away); stop immediately
			// rather than spinning on a nil event stream.
			if a.done {
				return
			}
			continue
		}

		switch ev := ev.(type) {
		case *tcell.EventKey:
			a.handleKey(ev)
		case *tcell.EventResize:
			a.model.width, a.model.height = a.screen.Size()
			a.screen.Sync()
		case *tcell.EventInterrupt:
			// A finished render: flush its escape sequence from the event
			// loop so it can never interleave with tcell's own output. Each
			// graphics renderer delivers its completed payload the same way.
			if b, ok := ev.Data().(*preview.KittyBurst); ok {
				if a.preview != nil {
					a.preview.FlushKitty(b)
				}
				continue
			}
			if b, ok := ev.Data().(*preview.SixelBurst); ok {
				if a.preview != nil {
					a.preview.FlushSixel(b)
				}
				continue
			}
			if b, ok := ev.Data().(*preview.ItermBurst); ok {
				if a.preview != nil {
					a.preview.FlushIterm(b)
				}
				continue
			}
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
	// quitOnce guards close(a.quit): doQuit can now be reached from both the
	// event loop (Ctrl+C / q) and the signal handler goroutine (SIGINT /
	// SIGTERM), so a double close would panic.
	a.quitOnce.Do(func() {
		if a.preview != nil {
			a.preview.Clear()
			a.preview.Close()
		}
		a.done = true
		close(a.quit)
		// Wake the event loop out of PollEvent so it can exit promptly even
		// if a render or kitty flush is in progress.
		_ = a.screen.PostEvent(tcell.NewEventInterrupt("quit"))
	})
}

func (a *App) handleSearchKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEnter:
		if len(a.model.searchInput) > 0 {
			a.model.searchQuery = string(a.model.searchInput)
			a.model.state = stateLoading
			a.render()
			go a.searchVideos()
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if ev.Modifiers() == tcell.ModCtrl {
			a.model.searchInput, a.model.searchPos = deleteWordBefore(a.model.searchInput, a.model.searchPos)
			a.model.searchScroll = 0
		} else if a.model.searchPos > 0 {
			a.model.searchInput = append(
				a.model.searchInput[:a.model.searchPos-1],
				a.model.searchInput[a.model.searchPos:]...,
			)
			a.model.searchPos--
			a.model.searchScroll = 0
		}
	case tcell.KeyDelete:
		if ev.Modifiers() == tcell.ModCtrl {
			a.model.searchInput, a.model.searchPos = deleteWordAfter(a.model.searchInput, a.model.searchPos)
		} else if a.model.searchPos < len(a.model.searchInput) {
			a.model.searchInput = append(
				a.model.searchInput[:a.model.searchPos],
				a.model.searchInput[a.model.searchPos+1:]...,
			)
		}
	case tcell.KeyLeft:
		if ev.Modifiers() == tcell.ModCtrl {
			a.model.searchPos = moveWordBackward(a.model.searchInput, a.model.searchPos)
		} else if a.model.searchPos > 0 {
			a.model.searchPos--
		}
	case tcell.KeyRight:
		if ev.Modifiers() == tcell.ModCtrl {
			a.model.searchPos = moveWordForward(a.model.searchInput, a.model.searchPos)
		} else if a.model.searchPos < len(a.model.searchInput) {
			a.model.searchPos++
		}
	case tcell.KeyCtrlB:
		a.model.searchInput, a.model.searchPos = deleteWordBefore(a.model.searchInput, a.model.searchPos)
		a.model.searchScroll = 0
	case tcell.KeyCtrlF:
		a.model.searchInput, a.model.searchPos = deleteWordAfter(a.model.searchInput, a.model.searchPos)
	case tcell.KeyHome:
		a.model.searchPos = 0
	case tcell.KeyEnd:
		a.model.searchPos = len(a.model.searchInput)
	case tcell.KeyCtrlC:
		a.doQuit()
	case tcell.KeyRune:
		r := ev.Rune()
		pos := a.model.searchPos
		// Insert rune at cursor position
		a.model.searchInput = append(a.model.searchInput, 0)
		copy(a.model.searchInput[pos+1:], a.model.searchInput[pos:])
		a.model.searchInput[pos] = r
		a.model.searchPos++
	}
}

// deleteWordBefore deletes the word before the cursor position
func deleteWordBefore(runes []rune, pos int) ([]rune, int) {
	if pos == 0 {
		return runes, pos
	}
	// Skip trailing spaces
	newPos := pos
	for newPos > 0 && runes[newPos-1] == ' ' {
		newPos--
	}
	// Delete word characters
	for newPos > 0 && runes[newPos-1] != ' ' {
		newPos--
	}
	return append(append([]rune(nil), runes[:newPos]...), runes[pos:]...), newPos
}

// deleteWordAfter deletes the word after the cursor position
func deleteWordAfter(runes []rune, pos int) ([]rune, int) {
	if pos >= len(runes) {
		return runes, pos
	}
	// Skip leading spaces
	newPos := pos
	for newPos < len(runes) && runes[newPos] == ' ' {
		newPos++
	}
	// Delete word characters
	for newPos < len(runes) && runes[newPos] != ' ' {
		newPos++
	}
	return append(append([]rune(nil), runes[:pos]...), runes[newPos:]...), pos
}

// moveWordBackward moves the cursor one word backward
func moveWordBackward(runes []rune, pos int) int {
	if pos == 0 {
		return 0
	}
	// Skip trailing spaces
	newPos := pos
	for newPos > 0 && runes[newPos-1] == ' ' {
		newPos--
	}
	// Skip word characters
	for newPos > 0 && runes[newPos-1] != ' ' {
		newPos--
	}
	return newPos
}

// moveWordForward moves the cursor one word forward
func moveWordForward(runes []rune, pos int) int {
	if pos >= len(runes) {
		return len(runes)
	}
	// Skip word characters
	newPos := pos
	for newPos < len(runes) && runes[newPos] != ' ' {
		newPos++
	}
	// Skip spaces
	for newPos < len(runes) && runes[newPos] == ' ' {
		newPos++
	}
	return newPos
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
			a.model.formats, a.model.selectedFmt = getDefaultFormats(a.cfg)
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
			a.model.searchPos = len(a.model.searchInput)
			a.model.searchScroll = 0
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

	stateChanged := a.model.state != a.prevState
	sizeChanged := a.model.width != a.prevWidth || a.model.height != a.prevHeight
	if stateChanged || sizeChanged {
		a.screen.Clear()
		a.prevState = a.model.state
		a.prevWidth = a.model.width
		a.prevHeight = a.model.height
	}

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

	// Fixed box width: 60 chars, or terminal width minus 4 if smaller
	maxBoxW := 60
	boxW := minInt(maxBoxW, maxInt(20, w-4))
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

	// Calculate visible portion of search input
	searchLabel := "Search: "
	visibleW := boxW - 4 - displayWidth(searchLabel) // account for label and padding
	if visibleW < 1 {
		visibleW = 1
	}

	// Calculate cursor position in character width
	cursorPos := 0
	for i := 0; i < a.model.searchPos && i < len(a.model.searchInput); i++ {
		cursorPos += displayWidth(string(a.model.searchInput[i]))
	}

	// Auto-scroll to show cursor when it moves outside visible area
	if cursorPos > a.model.searchScroll+visibleW {
		a.model.searchScroll = cursorPos - visibleW
	}
	if cursorPos < a.model.searchScroll {
		a.model.searchScroll = cursorPos
	}
	if a.model.searchScroll < 0 {
		a.model.searchScroll = 0
	}

	// Extract visible portion by character width and track rune positions
	var visibleInput strings.Builder
	width := 0
	skip := a.model.searchScroll
	cursorRuneIdx := 0 // rune index where cursor should be inserted in visible portion

	for i, r := range a.model.searchInput {
		rw := displayWidth(string(r))
		if skip > 0 {
			skip -= rw
			continue
		}
		if width+rw > visibleW {
			break
		}
		visibleInput.WriteRune(r)
		width += rw
		if i < a.model.searchPos {
			cursorRuneIdx++
		}
	}

	// Insert cursor character at the correct rune position
	visibleStr := visibleInput.String()
	var searchBox string
	if cursorRuneIdx <= len(visibleStr) {
		// Insert cursor at rune position
		searchBox = searchLabel + visibleStr[:cursorRuneIdx] + "▏" + visibleStr[cursorRuneIdx:]
	} else {
		searchBox = searchLabel + visibleStr + "▏"
	}

	// Truncate if needed
	if displayWidth(searchBox) > boxW-2 {
		searchBox = truncateByWidth(searchBox, boxW-2)
	}

	// Clear the search text row to remove stale characters from shorter previous text.
	for x := boxX + 1; x < boxX+boxW-1; x++ {
		a.screen.SetContent(x, searchY, ' ', nil, tcell.StyleDefault.
			Foreground(tcell.Color(0xe4e4e7)).
			Background(tcell.Color(0x0f0f1a)))
	}
	a.drawText(searchX, searchY, searchBox, tcell.Color(0xe4e4e7), tcell.Color(0x0f0f1a), false)

	help := "Press Enter to search • Ctrl+C to quit"
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
	if previewW == 0 {
		if a.preview != nil {
			a.preview.Clear()
		}
	} else {
		// Always fill preview area with dark background to clear stale tcell content.
		a.drawPreviewBackground(previewRect)
		if a.preview == nil || !a.preview.Supported() {
			a.drawPreviewPlaceholder(previewRect)
		}
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

func (a *App) drawPreviewBackground(rect preview.Rect) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			a.screen.SetContent(x, y, ' ', nil, tcell.StyleDefault.
				Background(tcell.Color(0x141421)))
		}
	}
}

func (a *App) drawPreviewPlaceholder(rect preview.Rect) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}

	a.drawPreviewBackground(rect)

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

	lineY := boxY + 3
	for i, f := range a.model.formats {
		mark := "○"
		if i == a.model.selectedFmt {
			mark = "◉"
		}
		line := fmt.Sprintf(" %s %s", mark, f.Label)
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
	// Force tcell to flush any pending output so the terminal is fully
	// quiescent — kitty graphics deleted, cursor state consistent — before
	// handing the terminal to mpv (issue: missing kitty graphics cleanup
	// before mpv).
	a.screen.Show()
	_ = a.screen.Suspend()

	origSelected := a.model.selected
	origVideos := a.model.videos

	for {
		if len(a.model.videos) == 0 || a.model.selected >= len(a.model.videos) {
			break
		}

		v := a.model.videos[a.model.selected]

		// Resolve quality from config; audioOnly toggle overrides.
		quality := config.ResolveQuality(a.cfg.Quality)
		qualityLabel := a.cfg.Quality
		if a.model.audioOnly {
			quality = "bestaudio/best"
			qualityLabel = "Audio"
		}

		fmt.Printf("▶ Now Playing: %s\n", v.Title)
		fmt.Printf("Quality: %s\n\n", qualityLabel)

		args := []string{
			"--term-osd=force",
			"--term-osd-bar",
			"--force-window=no",
			"--ytdl-format=" + quality,
		}
		if a.model.audioOnly {
			args = append(args, "--no-video")
		}
		args = append(args, a.cfg.MPV.Options...)
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

	// Restore original results — related videos never polluted the list.
	a.model.videos = origVideos
	a.model.selected = origSelected

	_ = a.screen.Resume()
	a.screen.SetStyle(tcell.StyleDefault.
		Background(tcell.Color(0x0f0f1a)).
		Foreground(tcell.Color(0xe4e4e7)))
	a.render()
}

func (a *App) fetchRelatedVideo(videoID string) (*scraper.Video, error) {
	relatedVideos, err := a.model.scraper.GetRelatedVideos(videoID, 20)
	if err != nil {
		return nil, err
	}

	if len(relatedVideos) == 0 {
		return nil, fmt.Errorf("no related videos found")
	}

	return &relatedVideos[0], nil
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
	// Flush everything (including the kitty graphics delete-all) before
	// handing the terminal to mpv (issue: missing kitty graphics cleanup).
	a.screen.Show()
	_ = a.screen.Suspend()

	fmt.Printf("▶ Now Playing: %s\n", v.Title)
	fmt.Printf("Quality: %s\n\n", f.Label)

	args := []string{
		"--term-osd=force",
		"--term-osd-bar",
		"--force-window=no",
		"--ytdl-format=" + f.Quality,
	}
	args = append(args, a.cfg.MPV.Options...)
	args = append(args, v.URL)

	cmd := exec.Command("mpv", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	_ = a.screen.Resume()
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

func NewModel(cfg *config.Config) *Model {
	s := scraper.NewYouTubeScraper()

	return &Model{
		state:    stateSearch,
		keymap:   DefaultKeyMap(),
		scraper:  s,
		autoplay: AutoplayOff,
	}
}

func getDefaultFormats(cfg *config.Config) ([]scraper.Stream, int) {
	formats := []scraper.Stream{
		{Label: "1080p", Quality: "bestvideo[height<=1080]+bestaudio/best[height<=1080]/best"},
		{Label: "720p", Quality: "bestvideo[height<=720]+bestaudio/best[height<=720]/best"},
		{Label: "480p", Quality: "bestvideo[height<=480]+bestaudio/best[height<=480]/best"},
		{Label: "360p", Quality: "bestvideo[height<=360]+bestaudio/best[height<=360]/best"},
		{Label: "Audio", Quality: "bestaudio/best"},
	}

	selectedIdx := 0 // default to 1080p

	// Find which preset matches the config quality and pre-select it.
	// Never creates a new entry — only highlights the matching one.
	if cfg != nil && cfg.Quality != "" {
		resolved := config.ResolveQuality(cfg.Quality)
		for i, f := range formats {
			if f.Quality == resolved {
				selectedIdx = i
				break
			}
		}
	}

	return formats, selectedIdx
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
