package interactioncomponent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	appcli "github.com/ingot-agent/app-cli"
	applicationruntime "github.com/ingot-agent/sdk/application"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"golang.org/x/term"
)

const (
	sidebarBreakpoint   = 100
	sidebarWidth        = 28
	markdownFrame       = time.Second / 30
	toolPreviewMaxBytes = 4 * 1024
)

var ErrTerminalRequired = errors.New("full-screen TUI requires terminal stdin and stdout; use `ingot chat --plain` for pipes or redirection")

type tuiFrontend struct {
	runCtx       context.Context
	cancel       context.CancelFunc
	process      applicationruntime.Process
	program      *tea.Program
	inputGate    chan struct{}
	interrupts   chan appcli.Interrupt
	ready        chan struct{}
	done         chan struct{}
	doneOnce     sync.Once
	releaseOnce  sync.Once
	releaseLease func()
	errMu        sync.Mutex
	runErr       error
	nextRequest  atomic.Uint64
}

type inputResult struct {
	text string
	err  error
}

type requestState struct {
	id       uint64
	response chan inputResult
	ask      *interaction.AskRequest
	selected int
	custom   bool
}

type transcriptKind uint8

const (
	blockUser transcriptKind = iota + 1
	blockAssistant
	blockTool
	blockStatus
	blockError
)

type transcriptBlock struct {
	kind       transcriptKind
	text       string
	toolID     string
	toolName   string
	arguments  json.RawMessage
	toolResult string
	toolDone   bool
}

type tuiModel struct {
	frontend *tuiFrontend

	width, height int
	mainWidth     int
	bodyHeight    int
	showSidebar   bool
	sidebarOpen   bool
	sidebarFocus  bool
	helpOpen      bool
	isDark        bool
	color         bool
	maxLine       int
	inputPrompt   string

	composer textarea.Model
	viewport viewport.Model

	current         session.ID
	sessions        []session.Summary
	selectedSession int
	blocks          []transcriptBlock
	activeAssistant bool
	busy            bool
	status          string
	followOutput    bool
	dirty           bool
	renderScheduled bool
	request         *requestState
}

type readyMsg struct{}
type renderTickMsg struct{}

type renderEventMsg struct {
	event interaction.Event
	ack   chan error
}

type syncViewMsg struct {
	view appcli.SessionView
	ack  chan error
}

type startTurnMsg struct {
	input string
	ack   chan error
}

type finishTurnMsg struct {
	state appcli.TurnState
	ack   chan error
}

type beginInputMsg struct {
	id       uint64
	prompt   string
	response chan inputResult
	ack      chan error
}

type beginAskMsg struct {
	id       uint64
	request  interaction.AskRequest
	response chan inputResult
	ack      chan error
}

type cancelInputMsg struct{ id uint64 }

func newTUI(ctx context.Context, cfg appcli.Config, process applicationruntime.Process) (Exports, func(context.Context) error, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return Exports{}, nil, ErrTerminalRequired
	}
	normalized, err := normalizeConfig(cfg.Interaction)
	if err != nil {
		return Exports{}, nil, err
	}
	releaseLease, ok := acquireTerminalLease()
	if !ok {
		return Exports{}, nil, ErrTerminalInUse
	}
	runCtx, cancel := context.WithCancel(ctx)
	frontend := &tuiFrontend{
		runCtx: runCtx, cancel: cancel, process: process,
		inputGate: make(chan struct{}, 1), interrupts: make(chan appcli.Interrupt, 4),
		ready: make(chan struct{}), done: make(chan struct{}), releaseLease: releaseLease,
	}
	frontend.inputGate <- struct{}{}
	model := newTUIModel(frontend, normalized, cfg.Interaction.InputPrompt)
	frontend.program = tea.NewProgram(model, tea.WithContext(runCtx), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout), tea.WithoutSignalHandler())
	go func() {
		_, runErr := frontend.program.Run()
		if errors.Is(runErr, tea.ErrProgramKilled) || errors.Is(runErr, context.Canceled) {
			runErr = nil
		}
		frontend.finish(runErr)
		if runErr != nil && ctx.Err() == nil {
			process.Shutdown(fmt.Errorf("run terminal UI: %w", runErr))
		}
	}()
	select {
	case <-frontend.ready:
		return Exports{Channel: frontend, Frontend: frontend}, frontend.cleanup, nil
	case <-frontend.done:
		frontend.release()
		if err := frontend.err(); err != nil {
			return Exports{}, nil, err
		}
		return Exports{}, nil, interaction.ErrUnavailable
	case <-ctx.Done():
		cancel()
		<-frontend.done
		frontend.release()
		return Exports{}, nil, ctx.Err()
	}
}

func newTUIModel(frontend *tuiFrontend, cfg normalizedConfig, inputPrompt string) *tuiModel {
	composer := textarea.New()
	composer.Prompt = inputPrompt
	if composer.Prompt == "" {
		composer.Prompt = "> "
	}
	composer.Placeholder = "Message ingot…"
	composer.ShowLineNumbers = false
	composer.MaxHeight = 8
	composer.SetHeight(3)
	composer.CharLimit = -1
	composer.SetVirtualCursor(true)
	viewportModel := viewport.New(viewport.WithWidth(80), viewport.WithHeight(16))
	viewportModel.SoftWrap = true
	viewportModel.FillHeight = true
	model := &tuiModel{
		frontend: frontend, width: 80, height: 24, mainWidth: 80, bodyHeight: 22,
		composer: composer, viewport: viewportModel, color: cfg.color, isDark: true,
		maxLine: cfg.maxLine, inputPrompt: composer.Prompt, followOutput: true,
	}
	model.applyStyles()
	model.layout()
	return model
}

func (m *tuiModel) Init() tea.Cmd {
	cmds := []tea.Cmd{func() tea.Msg { return readyMsg{} }}
	if m.color {
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	return tea.Batch(cmds...)
}

func (m *tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	switch msg := message.(type) {
	case readyMsg:
		select {
		case <-m.frontend.ready:
		default:
			close(m.frontend.ready)
		}
	case tea.BackgroundColorMsg:
		m.isDark = msg.IsDark()
		m.applyStyles()
		m.rebuildTranscript(true)
	case tea.WindowSizeMsg:
		m.width, m.height = max(20, msg.Width), max(8, msg.Height)
		m.layout()
		m.rebuildTranscript(false)
	case renderTickMsg:
		m.renderScheduled = false
		if m.dirty {
			m.rebuildTranscript(false)
		}
	case renderEventMsg:
		err := m.applyEvent(msg.event)
		ack(msg.ack, err)
		if err == nil {
			commands = append(commands, m.scheduleRender())
		}
	case syncViewMsg:
		m.applySessionView(msg.view)
		m.rebuildTranscript(true)
		ack(msg.ack, nil)
	case startTurnMsg:
		m.blocks = append(m.blocks, transcriptBlock{kind: blockUser, text: sanitizeText(msg.input)})
		m.activeAssistant = false
		m.busy = true
		m.status = "working…  Ctrl+C cancel"
		m.composer.Reset()
		m.composer.Blur()
		m.rebuildTranscript(true)
		ack(msg.ack, nil)
	case finishTurnMsg:
		m.busy = false
		m.activeAssistant = false
		switch msg.state {
		case appcli.TurnCanceled:
			m.status = "turn canceled"
		case appcli.TurnFailed:
			m.status = "turn failed"
		default:
			m.status = "ready"
		}
		m.rebuildTranscript(true)
		ack(msg.ack, nil)
	case beginInputMsg:
		if m.request != nil {
			ack(msg.ack, interaction.ErrUnavailable)
			break
		}
		m.request = &requestState{id: msg.id, response: msg.response}
		m.composer.Prompt = msg.prompt
		m.composer.Reset()
		m.composer.Focus()
		m.status = "ready"
		m.layout()
		ack(msg.ack, nil)
	case beginAskMsg:
		if m.request != nil {
			ack(msg.ack, interaction.ErrUnavailable)
			break
		}
		request := msg.request
		m.request = &requestState{id: msg.id, response: msg.response, ask: &request}
		m.composer.Prompt = "? "
		m.composer.Reset()
		if len(request.Options) == 0 {
			m.composer.Focus()
		} else {
			m.composer.Blur()
		}
		m.status = "answer required"
		m.layout()
		ack(msg.ack, nil)
	case cancelInputMsg:
		if m.request != nil && m.request.id == msg.id {
			m.request = nil
			m.composer.Reset()
			m.composer.Blur()
		}
	case tea.KeyPressMsg:
		commands = append(commands, m.handleKey(msg))
	case tea.MouseWheelMsg:
		if !m.helpOpen && !(m.sidebarOpen && !m.showSidebar) && m.requestAskWithOptions() == nil {
			before := m.viewport.AtBottom()
			updated, cmd := m.viewport.Update(msg)
			m.viewport = updated
			commands = append(commands, cmd)
			m.followOutput = before && m.viewport.AtBottom()
		}
	default:
		if m.composer.Focused() {
			before := m.composer.Value()
			updated, cmd := m.composer.Update(msg)
			m.composer = updated
			if len([]byte(m.composer.Value())) > m.maxLine {
				m.composer.SetValue(before)
				m.status = fmt.Sprintf("input limit: %d bytes", m.maxLine)
			}
			commands = append(commands, cmd)
			m.layout()
		}
	}
	return m, tea.Batch(commands...)
}

func (m *tuiModel) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.Keystroke()
	if key == "f1" {
		m.helpOpen = !m.helpOpen
		return nil
	}
	if m.helpOpen {
		if key == "esc" || key == "q" || key == "f1" {
			m.helpOpen = false
		}
		return nil
	}
	if m.request != nil && m.request.ask != nil {
		return m.handleAskKey(key, msg)
	}
	if key == "ctrl+q" {
		if m.busy {
			m.emitInterrupt(appcli.InterruptExit)
		} else {
			m.submitLine("/exit")
		}
		return nil
	}
	if m.busy {
		if key == "ctrl+c" || key == "esc" {
			m.emitInterrupt(appcli.InterruptCancel)
			m.status = "canceling…"
		}
		return nil
	}
	if m.sidebarOpen || m.sidebarFocus {
		return m.handleSidebarKey(key)
	}
	switch key {
	case "ctrl+o":
		if m.showSidebar {
			m.sidebarFocus = true
			m.composer.Blur()
		} else {
			m.sidebarOpen = true
		}
		return nil
	case "ctrl+n":
		m.submitLine("/new")
		return nil
	case "tab":
		if m.showSidebar {
			m.sidebarFocus = true
			m.composer.Blur()
		}
		return nil
	case "ctrl+c":
		if m.composer.Value() != "" {
			m.composer.Reset()
			m.status = "input cleared"
		} else {
			m.submitLine("/exit")
		}
		return nil
	case "enter":
		m.submitLine(m.composer.Value())
		return nil
	case "shift+enter", "alt+enter", "ctrl+j":
		before := m.composer.Value()
		m.composer.InsertString("\n")
		if len([]byte(m.composer.Value())) > m.maxLine {
			m.composer.SetValue(before)
			m.status = fmt.Sprintf("input limit: %d bytes", m.maxLine)
		}
		m.layout()
		return nil
	case "pgup", "pgdown", "home", "end":
		before := m.viewport.AtBottom()
		updated, cmd := m.viewport.Update(msg)
		m.viewport = updated
		m.followOutput = before && m.viewport.AtBottom()
		return cmd
	}
	before := m.composer.Value()
	updated, cmd := m.composer.Update(msg)
	m.composer = updated
	if len([]byte(m.composer.Value())) > m.maxLine {
		m.composer.SetValue(before)
		m.status = fmt.Sprintf("input limit: %d bytes", m.maxLine)
	}
	m.layout()
	return cmd
}

func (m *tuiModel) handleAskKey(key string, msg tea.KeyPressMsg) tea.Cmd {
	request := m.request.ask
	if key == "ctrl+q" {
		m.emitInterrupt(appcli.InterruptExit)
		return nil
	}
	if key == "ctrl+c" || key == "esc" {
		m.emitInterrupt(appcli.InterruptCancel)
		m.status = "canceling…"
		return nil
	}
	if len(request.Options) > 0 && !m.request.custom {
		count := len(request.Options)
		if request.AllowTextInput {
			count++
		}
		switch key {
		case "up", "k":
			m.request.selected = (m.request.selected - 1 + count) % count
			return nil
		case "down", "j":
			m.request.selected = (m.request.selected + 1) % count
			return nil
		case "enter":
			if m.request.selected < len(request.Options) {
				m.answer(request.Options[m.request.selected].Label)
			} else {
				m.request.custom = true
				m.composer.Reset()
				m.composer.Focus()
			}
			return nil
		}
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			index := int(key[0] - '1')
			if index < count {
				m.request.selected = index
			}
			return nil
		}
		return nil
	}
	if key == "enter" {
		if m.request.custom && strings.TrimSpace(m.composer.Value()) == "" {
			m.status = "please enter a response"
			return nil
		}
		m.answer(m.composer.Value())
		return nil
	}
	if key == "shift+enter" || key == "alt+enter" || key == "ctrl+j" {
		m.composer.InsertString("\n")
		return nil
	}
	updated, cmd := m.composer.Update(msg)
	m.composer = updated
	return cmd
}

func (m *tuiModel) handleSidebarKey(key string) tea.Cmd {
	switch key {
	case "esc", "ctrl+o":
		m.sidebarOpen = false
		m.sidebarFocus = false
		if m.request != nil {
			m.composer.Focus()
		}
	case "tab":
		if m.showSidebar {
			m.sidebarFocus = false
			if m.request != nil {
				m.composer.Focus()
			}
		}
	case "up", "k":
		if len(m.sessions) > 0 {
			m.selectedSession = (m.selectedSession - 1 + len(m.sessions)) % len(m.sessions)
		}
	case "down", "j":
		if len(m.sessions) > 0 {
			m.selectedSession = (m.selectedSession + 1) % len(m.sessions)
		}
	case "enter":
		if len(m.sessions) > 0 {
			id := m.sessions[m.selectedSession].ID
			m.sidebarOpen = false
			m.sidebarFocus = false
			m.submitLine("/use " + string(id))
		}
	case "ctrl+n":
		m.sidebarOpen = false
		m.sidebarFocus = false
		m.submitLine("/new")
	case "ctrl+q":
		m.submitLine("/exit")
	}
	return nil
}

func (m *tuiModel) submitLine(value string) {
	if m.request == nil || m.request.ask != nil {
		return
	}
	if strings.TrimSpace(value) == "" || len([]byte(value)) > m.maxLine {
		return
	}
	response := m.request.response
	m.request = nil
	m.composer.Reset()
	m.composer.Blur()
	select {
	case response <- inputResult{text: value}:
	default:
	}
}

func (m *tuiModel) answer(value string) {
	response := m.request.response
	m.request = nil
	m.composer.Reset()
	m.composer.Blur()
	select {
	case response <- inputResult{text: value}:
	default:
	}
}

func (m *tuiModel) emitInterrupt(kind appcli.InterruptKind) {
	select {
	case m.frontend.interrupts <- appcli.Interrupt{Kind: kind}:
	default:
	}
}

func (m *tuiModel) requestAskWithOptions() *interaction.AskRequest {
	if m.request == nil || m.request.ask == nil || len(m.request.ask.Options) == 0 || m.request.custom {
		return nil
	}
	return m.request.ask
}

func (m *tuiModel) applyEvent(event interaction.Event) error {
	switch value := event.(type) {
	case interaction.TextEvent:
		text := sanitizeText(value.Text)
		if text == "" {
			return nil
		}
		if m.activeAssistant && len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].kind == blockAssistant {
			m.blocks[len(m.blocks)-1].text += text
		} else {
			m.blocks = append(m.blocks, transcriptBlock{kind: blockAssistant, text: text})
			m.activeAssistant = true
		}
	case interaction.StatusEvent:
		m.blocks = append(m.blocks, transcriptBlock{kind: blockStatus, text: sanitizeText(value.Text)})
		m.activeAssistant = false
	case interaction.ErrorEvent:
		if value.Err != nil {
			m.blocks = append(m.blocks, transcriptBlock{kind: blockError, text: sanitizeText(value.Err.Error())})
		}
		m.activeAssistant = false
	case interaction.ToolCallEvent:
		m.blocks = append(m.blocks, transcriptBlock{
			kind: blockTool, toolID: sanitizeText(value.Call.ID), toolName: sanitizeText(value.Call.Name),
			arguments: append(json.RawMessage(nil), value.Call.Arguments...),
		})
		m.activeAssistant = false
	case interaction.ToolResultEvent:
		attached := false
		for index := len(m.blocks) - 1; index >= 0; index-- {
			if m.blocks[index].kind == blockTool && m.blocks[index].toolID == value.Call.ID {
				m.blocks[index].toolResult = sanitizeText(value.Result.Content)
				m.blocks[index].toolDone = true
				attached = true
				break
			}
		}
		if !attached {
			m.blocks = append(m.blocks, transcriptBlock{
				kind: blockTool, toolID: sanitizeText(value.Call.ID), toolName: sanitizeText(value.Call.Name),
				toolResult: sanitizeText(value.Result.Content), toolDone: true,
			})
		}
		m.activeAssistant = false
	default:
		return fmt.Errorf("unsupported interaction event %T", event)
	}
	m.dirty = true
	return nil
}

func (m *tuiModel) applySessionView(view appcli.SessionView) {
	m.current = view.Current
	m.sessions = append([]session.Summary(nil), view.Sessions...)
	m.selectedSession = 0
	for index, summary := range m.sessions {
		if summary.ID == m.current {
			m.selectedSession = index
			break
		}
	}
	m.blocks = blocksFromHistory(view.Messages)
	m.activeAssistant = false
	m.dirty = true
}

func blocksFromHistory(messages []model.Message) []transcriptBlock {
	blocks := make([]transcriptBlock, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case model.RoleUser:
			blocks = append(blocks, transcriptBlock{kind: blockUser, text: sanitizeText(message.Content)})
		case model.RoleAssistant:
			if message.Content != "" {
				blocks = append(blocks, transcriptBlock{kind: blockAssistant, text: sanitizeText(message.Content)})
			}
			for _, call := range message.ToolCalls {
				blocks = append(blocks, transcriptBlock{
					kind: blockTool, toolID: sanitizeText(call.ID), toolName: sanitizeText(call.Name),
					arguments: append(json.RawMessage(nil), call.Arguments...),
				})
			}
		case model.RoleTool:
			attached := false
			for index := len(blocks) - 1; index >= 0; index-- {
				if blocks[index].kind == blockTool && blocks[index].toolID == message.ToolCallID {
					blocks[index].toolResult = sanitizeText(message.Content)
					blocks[index].toolDone = true
					attached = true
					break
				}
			}
			if !attached {
				blocks = append(blocks, transcriptBlock{kind: blockTool, toolID: sanitizeText(message.ToolCallID), toolResult: sanitizeText(message.Content), toolDone: true})
			}
		}
	}
	return blocks
}

func (m *tuiModel) scheduleRender() tea.Cmd {
	if m.renderScheduled {
		return nil
	}
	m.renderScheduled = true
	return tea.Tick(markdownFrame, func(time.Time) tea.Msg { return renderTickMsg{} })
}

func (m *tuiModel) rebuildTranscript(forceBottom bool) {
	width := max(20, m.mainWidth-4)
	parts := make([]string, 0, len(m.blocks))
	for _, block := range m.blocks {
		parts = append(parts, m.renderBlock(block, width))
	}
	if len(parts) == 0 {
		parts = append(parts, m.mutedStyle().Render("Start a new conversation, or press Ctrl+O to open a previous session."))
	}
	content := strings.Join(parts, "\n\n")
	follow := forceBottom || m.followOutput || m.viewport.AtBottom()
	m.viewport.SetContent(content)
	if follow {
		m.viewport.GotoBottom()
		m.followOutput = true
	}
	m.dirty = false
}

func (m *tuiModel) renderBlock(block transcriptBlock, width int) string {
	switch block.kind {
	case blockUser:
		return m.labelStyle("user").Render("You") + "\n" + lipgloss.Wrap(block.text, width, "")
	case blockAssistant:
		body := m.renderMarkdown(block.text, width)
		return m.labelStyle("assistant").Render("Assistant") + "\n" + strings.TrimSpace(body)
	case blockTool:
		state := "running"
		if block.toolDone {
			state = "done"
		}
		header := fmt.Sprintf("Tool · %s · %s", emptyFallback(block.toolName, block.toolID), state)
		arguments := compactJSON(block.arguments)
		arguments, argumentsCut := truncateUTF8(arguments, toolPreviewMaxBytes)
		body := "arguments: " + arguments
		if argumentsCut {
			body += " …[truncated]"
		}
		if block.toolDone {
			result, resultCut := truncateUTF8(block.toolResult, toolPreviewMaxBytes)
			body += "\nresult: " + result
			if resultCut {
				body += " …[truncated]"
			}
		}
		return m.toolStyle().Render(lipgloss.Wrap(header+"\n"+body, width-2, ""))
	case blockError:
		return m.errorStyle().Render(lipgloss.Wrap("Error · "+block.text, width, ""))
	default:
		return m.mutedStyle().Render(lipgloss.Wrap(block.text, width, ""))
	}
}

func (m *tuiModel) renderMarkdown(source string, width int) string {
	return renderTerminalMarkdown(m, source, width)
}

func (m *tuiModel) layout() {
	m.showSidebar = m.width >= sidebarBreakpoint
	if m.showSidebar {
		m.mainWidth = max(20, m.width-sidebarWidth-1)
	} else {
		m.mainWidth = m.width
		m.sidebarFocus = false
	}
	m.bodyHeight = max(6, m.height-2)
	m.composer.SetWidth(max(10, m.mainWidth-6))
	composerRows := strings.Count(m.composer.Value(), "\n") + 1
	m.composer.SetHeight(min(8, max(3, composerRows)))
	composerHeight := m.composer.Height() + 2
	m.viewport.SetWidth(max(10, m.mainWidth-2))
	m.viewport.SetHeight(max(1, m.bodyHeight-composerHeight))
}

func (m *tuiModel) applyStyles() {
	m.composer.SetStyles(textarea.DefaultStyles(m.isDark))
}

func (m *tuiModel) View() tea.View {
	m.layout()
	headerText := "ingot"
	if m.current != "" {
		headerText += "  " + string(m.current)
	} else {
		headerText += "  new conversation"
	}
	if m.busy {
		headerText += "  • working"
	}
	header := m.headerStyle().Width(m.width).Render(headerText)
	main := m.renderMain()
	if m.showSidebar {
		sidebar := m.renderSidebar(m.bodyHeight)
		main = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, m.mutedStyle().Render("│"), main)
	}
	footerText := m.status
	if footerText == "" {
		footerText = "Enter send · Ctrl+J newline · Ctrl+O sessions · F1 help"
	}
	if !m.followOutput {
		footerText = "new output below · " + footerText
	}
	footer := m.footerStyle().Width(m.width).Render(footerText)
	view := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, header, main, footer))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "ingot chat"
	return view
}

func (m *tuiModel) renderMain() string {
	width := m.mainWidth
	if m.helpOpen {
		return m.panelStyle().Width(width - 2).Height(m.bodyHeight - 2).Render(helpText())
	}
	if m.sidebarOpen && !m.showSidebar {
		return m.panelStyle().Width(width - 2).Height(m.bodyHeight - 2).Render(m.renderSidebarList(width-4, m.bodyHeight-4))
	}
	if request := m.requestAskWithOptions(); request != nil {
		return m.panelStyle().Width(width - 2).Height(m.bodyHeight - 2).Render(m.renderAskOptions(*request, width-6))
	}
	viewportView := m.viewport.View()
	composerView := ""
	if m.request != nil && (!m.busy || m.request.ask != nil) {
		composerView = m.composerStyle().Width(width - 2).Render(m.composer.View())
	} else {
		composerView = m.composerStyle().Width(width - 2).Render(m.mutedStyle().Render("Waiting for the current turn…"))
	}
	if m.request != nil && m.request.ask != nil {
		prompt := m.labelStyle("assistant").Render("Question") + "\n" + lipgloss.Wrap(sanitizeText(m.request.ask.Prompt), width-4, "")
		viewportView = m.panelStyle().Width(width - 2).Render(prompt)
	}
	return lipgloss.JoinVertical(lipgloss.Left, viewportView, composerView)
}

func (m *tuiModel) renderSidebar(height int) string {
	content := m.renderSidebarList(sidebarWidth-4, height-2)
	style := m.sidebarStyle()
	if m.sidebarFocus {
		style = style.BorderForeground(m.accentColor())
	}
	return style.Width(sidebarWidth - 2).Height(height - 2).Render(content)
}

func (m *tuiModel) renderSidebarList(width, height int) string {
	lines := []string{m.labelStyle("assistant").Render("Sessions")}
	if len(m.sessions) == 0 {
		lines = append(lines, "", m.mutedStyle().Render("No saved sessions"))
		return strings.Join(lines, "\n")
	}
	visible := max(1, height-2)
	start := 0
	if m.selectedSession >= visible {
		start = m.selectedSession - visible + 1
	}
	end := min(len(m.sessions), start+visible)
	for index := start; index < end; index++ {
		summary := m.sessions[index]
		marker := "  "
		if summary.ID == m.current {
			marker = "• "
		}
		line := marker + sanitizeText(emptyFallback(summary.Title, string(summary.ID)))
		line = lipgloss.Wrap(line, width, "")
		if index == m.selectedSession && (m.sidebarFocus || m.sidebarOpen) {
			line = m.selectedStyle().Width(width).Render(line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *tuiModel) renderAskOptions(request interaction.AskRequest, width int) string {
	lines := []string{m.labelStyle("assistant").Render("Question"), lipgloss.Wrap(sanitizeText(request.Prompt), width, ""), ""}
	for index, option := range request.Options {
		prefix := "  "
		if index == m.request.selected {
			prefix = "› "
		}
		line := fmt.Sprintf("%s%d. %s", prefix, index+1, sanitizeText(option.Label))
		if option.Description != "" {
			line += "\n    " + sanitizeText(option.Description)
		}
		lines = append(lines, line)
	}
	if request.AllowTextInput {
		prefix := "  "
		if m.request.selected == len(request.Options) {
			prefix = "› "
		}
		lines = append(lines, fmt.Sprintf("%s%d. Other…", prefix, len(request.Options)+1))
	}
	lines = append(lines, "", m.mutedStyle().Render("↑/↓ select · Enter confirm · Esc cancel"))
	return strings.Join(lines, "\n")
}

func helpText() string {
	return strings.Join([]string{
		"Keyboard help", "",
		"Enter        Send message", "Ctrl+J       Insert newline", "Ctrl+N       New session",
		"Ctrl+O       Open sessions", "Tab          Switch sidebar/input", "PgUp/PgDn   Scroll transcript",
		"Ctrl+C       Cancel turn, clear input, or exit", "Ctrl+Q       Exit", "F1 / Esc     Close help",
		"", "Commands: /new [title]  /rename <title>  /use <id>  /list  /help  /exit",
	}, "\n")
}

func (m *tuiModel) accentColor() color.Color { return lipgloss.Color("39") }

func (m *tuiModel) headerStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	if m.color {
		style = style.Foreground(lipgloss.Color("255")).Background(lipgloss.Color("24"))
	}
	return style
}

func (m *tuiModel) footerStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Padding(0, 1)
	if m.color {
		style = style.Foreground(lipgloss.Color("244"))
	}
	return style
}

func (m *tuiModel) labelStyle(kind string) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	if !m.color {
		return style
	}
	switch kind {
	case "user":
		return style.Foreground(lipgloss.Color("75"))
	default:
		return style.Foreground(m.accentColor())
	}
}

func (m *tuiModel) mutedStyle() lipgloss.Style {
	style := lipgloss.NewStyle()
	if m.color {
		style = style.Foreground(lipgloss.Color("244"))
	}
	return style
}

func (m *tuiModel) errorStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	if m.color {
		style = style.Foreground(lipgloss.Color("196"))
	}
	return style
}

func (m *tuiModel) toolStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if m.color {
		style = style.BorderForeground(lipgloss.Color("240")).Foreground(lipgloss.Color("250"))
	}
	return style
}

func (m *tuiModel) composerStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if m.color {
		style = style.BorderForeground(m.accentColor())
	}
	return style
}

func (m *tuiModel) panelStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
	if m.color {
		style = style.BorderForeground(m.accentColor())
	}
	return style
}

func (m *tuiModel) sidebarStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if m.color {
		style = style.BorderForeground(lipgloss.Color("240"))
	}
	return style
}

func (m *tuiModel) selectedStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	if m.color {
		style = style.Foreground(lipgloss.Color("255")).Background(lipgloss.Color("24"))
	}
	return style
}

func (t *tuiFrontend) ReadLine(ctx context.Context, prompt string) (string, error) {
	return t.withInput(ctx, func(callCtx context.Context) (string, error) {
		id := t.nextRequest.Add(1)
		response := make(chan inputResult, 1)
		acknowledged := make(chan error, 1)
		if err := t.send(callCtx, beginInputMsg{id: id, prompt: prompt, response: response, ack: acknowledged}, acknowledged); err != nil {
			return "", err
		}
		select {
		case result := <-response:
			return result.text, result.err
		case <-callCtx.Done():
			t.program.Send(cancelInputMsg{id: id})
			return "", callCtx.Err()
		case <-t.done:
			if err := t.err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
	})
}

func (t *tuiFrontend) Ask(ctx context.Context, request interaction.AskRequest) (interaction.AskResponse, error) {
	if err := validateAskRequest(request); err != nil {
		return interaction.AskResponse{}, err
	}
	text, err := t.withInput(ctx, func(callCtx context.Context) (string, error) {
		id := t.nextRequest.Add(1)
		response := make(chan inputResult, 1)
		acknowledged := make(chan error, 1)
		request.Options = append([]interaction.AskOption(nil), request.Options...)
		if err := t.send(callCtx, beginAskMsg{id: id, request: request, response: response, ack: acknowledged}, acknowledged); err != nil {
			return "", err
		}
		select {
		case result := <-response:
			return result.text, result.err
		case <-callCtx.Done():
			t.program.Send(cancelInputMsg{id: id})
			return "", callCtx.Err()
		case <-t.done:
			if err := t.err(); err != nil {
				return "", err
			}
			return "", interaction.ErrUnavailable
		}
	})
	if err != nil {
		return interaction.AskResponse{}, err
	}
	return interaction.AskResponse{Text: text}, nil
}

func (t *tuiFrontend) Render(ctx context.Context, event interaction.Event) error {
	if ctx == nil || isNil(event) {
		return interaction.ErrUnavailable
	}
	event = cloneInteractionEvent(event)
	acknowledged := make(chan error, 1)
	return t.send(ctx, renderEventMsg{event: event, ack: acknowledged}, acknowledged)
}

func (t *tuiFrontend) Sync(ctx context.Context, view appcli.SessionView) error {
	view = cloneSessionView(view)
	acknowledged := make(chan error, 1)
	return t.send(ctx, syncViewMsg{view: view, ack: acknowledged}, acknowledged)
}

func (t *tuiFrontend) StartTurn(ctx context.Context, input string) error {
	acknowledged := make(chan error, 1)
	return t.send(ctx, startTurnMsg{input: input, ack: acknowledged}, acknowledged)
}

func (t *tuiFrontend) FinishTurn(ctx context.Context, state appcli.TurnState) error {
	acknowledged := make(chan error, 1)
	return t.send(ctx, finishTurnMsg{state: state, ack: acknowledged}, acknowledged)
}

func (t *tuiFrontend) Interrupts() <-chan appcli.Interrupt { return t.interrupts }

func (t *tuiFrontend) withInput(ctx context.Context, operation func(context.Context) (string, error)) (string, error) {
	if ctx == nil {
		return "", interaction.ErrUnavailable
	}
	callCtx, cancel := mergeContext(ctx, t.runCtx)
	defer cancel()
	select {
	case <-callCtx.Done():
		return "", callCtx.Err()
	case <-t.inputGate:
	}
	defer func() { t.inputGate <- struct{}{} }()
	return operation(callCtx)
}

func (t *tuiFrontend) send(ctx context.Context, message tea.Msg, acknowledged <-chan error) error {
	if ctx == nil {
		return interaction.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-t.done:
		if err := t.err(); err != nil {
			return err
		}
		return interaction.ErrUnavailable
	default:
	}
	t.program.Send(message)
	select {
	case err := <-acknowledged:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		if err := t.err(); err != nil {
			return err
		}
		return interaction.ErrUnavailable
	}
}

func (t *tuiFrontend) cleanup(ctx context.Context) error {
	t.cancel()
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-t.done:
		t.release()
		return t.err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *tuiFrontend) finish(err error) {
	t.errMu.Lock()
	t.runErr = err
	t.errMu.Unlock()
	t.doneOnce.Do(func() {
		close(t.interrupts)
		close(t.done)
	})
	t.release()
}

func (t *tuiFrontend) release() {
	t.releaseOnce.Do(func() {
		if t.releaseLease != nil {
			t.releaseLease()
		}
	})
}

func (t *tuiFrontend) err() error {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.runErr
}

func ack(channel chan<- error, err error) {
	select {
	case channel <- err:
	default:
	}
}

func cloneSessionView(view appcli.SessionView) appcli.SessionView {
	view.Sessions = append([]session.Summary(nil), view.Sessions...)
	view.Messages = cloneModelMessages(view.Messages)
	return view
}

func cloneModelMessages(messages []model.Message) []model.Message {
	result := make([]model.Message, len(messages))
	for index, message := range messages {
		result[index] = message
		result[index].ToolCalls = make([]tool.Call, len(message.ToolCalls))
		for callIndex, call := range message.ToolCalls {
			result[index].ToolCalls[callIndex] = call
			result[index].ToolCalls[callIndex].Arguments = append(json.RawMessage(nil), call.Arguments...)
		}
	}
	return result
}

func cloneInteractionEvent(event interaction.Event) interaction.Event {
	switch value := event.(type) {
	case interaction.ToolCallEvent:
		value.Call.Arguments = append(json.RawMessage(nil), value.Call.Arguments...)
		return value
	case interaction.ToolResultEvent:
		value.Call.Arguments = append(json.RawMessage(nil), value.Call.Arguments...)
		return value
	default:
		return event
	}
}

func emptyFallback(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}

func truncateUTF8(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	prefix := []byte(value)[:maxBytes]
	for len(prefix) > 0 && !utf8.Valid(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return string(prefix), true
}

func sanitizeText(value string) string {
	var output strings.Builder
	for index := 0; index < len(value); {
		if value[index] == 0x1b {
			index++
			if index >= len(value) {
				break
			}
			switch value[index] {
			case '[':
				index++
				for index < len(value) {
					character := value[index]
					index++
					if character >= 0x40 && character <= 0x7e {
						break
					}
				}
			case ']':
				index++
				for index < len(value) {
					if value[index] == 0x07 {
						index++
						break
					}
					if value[index] == 0x1b && index+1 < len(value) && value[index+1] == '\\' {
						index += 2
						break
					}
					index++
				}
			default:
				index++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if r == utf8.RuneError && size == 1 {
			index++
			continue
		}
		index += size
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		output.WriteRune(r)
	}
	return output.String()
}

var _ interaction.Channel = (*tuiFrontend)(nil)
var _ appcli.Frontend = (*tuiFrontend)(nil)
