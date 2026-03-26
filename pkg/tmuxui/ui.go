package tmuxui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"tmux-ssh-manager/pkg/sshconfig"
	"tmux-ssh-manager/pkg/state"
	"tmux-ssh-manager/pkg/termio"
)

type App struct {
	Hosts          []sshconfig.Host
	State          *state.Store
	StatePath      string
	StartInSearch  bool
	ImplicitSelect bool
	EnterMode      string
	AddHost        func(sshconfig.AddHostInput) error
	ExecCredential func(string, string, string, string) (*exec.Cmd, error)
	InTmux         func() bool
	Connect        func(string) *exec.Cmd
	NewWindow      func(string) error
	SplitVert      func(string) error
	SplitHoriz     func(string) error
	Tiled          func([]string, string) error
	SetupLogging   func(string)
}

func (a App) Run() error {
	// Disable terminal capability probing while the picker is running.
	//
	// Lipgloss (via termenv) can trigger terminal OSC/DSR queries (e.g. OSC 11,
	// DSR cursor position). Some terminals write the responses back onto stdin.
	// If those responses escape the TUI lifecycle, they can be interpreted as
	// user input by the next exec'd program (ssh) or your shell.
	restore := disableTermQueries()
	defer restore()

	program := tea.NewProgram(
		newModel(a),
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
		tea.WithAltScreen(),
	)
	defer func() { _ = program.ReleaseTerminal() }()

	final, err := program.Run()
	if err != nil {
		return err
	}
	if m, ok := final.(model); ok && m.execAfterExit != nil {
		termio.SanitizeStdinBeforeExec(os.Stdin, os.Stderr)
		return m.execAfterExit.Run()
	}
	return nil
}

type candidate struct {
	host   sshconfig.Host
	search string
	line   string
}

type addHostModel struct {
	alias        textinput.Model
	hostName     textinput.Model
	user         textinput.Model
	port         textinput.Model
	proxyJump    textinput.Model
	identityFile textinput.Model
	field        int
	status       string
}

type credentialModel struct {
	action string
	host   string
	user   textinput.Model
	kind   textinput.Model
	field  int
	status string
}

type model struct {
	app             App
	input           textinput.Model
	add             addHostModel
	credential      credentialModel
	candidates      []candidate
	filtered        []candidate
	selected        int
	scroll          int
	selectedAliases map[string]struct{}
	filterFavorites bool
	filterRecents   bool
	showAddHost     bool
	showCredential  bool
	status          string
	width           int
	height          int
	pendingG        bool
	quitting        bool
	execAfterExit   *exec.Cmd
	helpStyle       lipgloss.Style
	statusStyle     lipgloss.Style
	selectedStyle   lipgloss.Style
	favoriteStyle   lipgloss.Style
	dimStyle        lipgloss.Style
}

type errMsg struct{ err error }
type actionMsg struct{ text string }

func newModel(app App) model {
	search := textinput.New()
	search.Prompt = "/ "
	search.Placeholder = "search hosts"
	search.Blur()

	newField := func(prompt, placeholder string) textinput.Model {
		field := textinput.New()
		field.Prompt = prompt
		field.Placeholder = placeholder
		field.CharLimit = 512
		field.Blur()
		return field
	}

	m := model{
		app:             app,
		input:           search,
		candidates:      buildCandidates(app.Hosts),
		selectedAliases: map[string]struct{}{},
		helpStyle:       lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		statusStyle:     lipgloss.NewStyle().Foreground(lipgloss.Color("86")),
		selectedStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("24")),
		favoriteStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("220")),
		dimStyle:        lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
	m.add.alias = newField("Alias: ", "edge1")
	m.add.hostName = newField("HostName: ", "10.0.0.10")
	m.add.user = newField("User: ", "optional")
	m.add.port = newField("Port: ", "22")
	m.add.proxyJump = newField("ProxyJump: ", "optional")
	m.add.identityFile = newField("IdentityFile: ", "optional")
	m.credential.user = newField("User: ", "optional")
	m.credential.kind = newField("Kind: ", "password")
	m.credential.kind.SetValue("password")
	m.recompute()
	if app.StartInSearch {
		m.input.Focus()
	}
	return m
}

func buildCandidates(hosts []sshconfig.Host) []candidate {
	out := make([]candidate, 0, len(hosts))
	for _, host := range hosts {
		parts := []string{host.Alias}
		if host.User != "" {
			parts = append(parts, "as "+host.User)
		}
		if host.Port > 0 && host.Port != 22 {
			parts = append(parts, fmt.Sprintf(":%d", host.Port))
		}
		if host.ProxyJump != "" {
			parts = append(parts, "via "+host.ProxyJump)
		}
		if host.HostName != "" && host.HostName != host.Alias {
			parts = append(parts, "-> "+host.HostName)
		}
		search := strings.ToLower(strings.Join([]string{host.Alias, host.HostName, host.User, host.ProxyJump}, " "))
		out = append(out, candidate{host: host, search: search, line: strings.Join(parts, " ")})
	}
	return out
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case errMsg:
		m.status = msg.err.Error()
		return m, nil
	case actionMsg:
		m.status = msg.text
		return m, nil
	case tea.KeyMsg:
		if m.showAddHost {
			return m.handleAddHost(msg)
		}
		if m.showCredential {
			return m.handleCredential(msg)
		}
		return m.handlePicker(msg)
	}
	return m, nil
}

func (m model) handlePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.input.Focused() {
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "esc":
			m.input.Blur()
			return m, nil
		case "up":
			m.move(-1)
			return m, nil
		case "down":
			m.move(1)
			return m, nil
		case "enter":
			if m.app.ImplicitSelect {
				m.input.Blur()
				m.recompute()
				return m.enterDefault()
			}
			m.input.Blur()
			m.recompute()
			return m, nil
		case "ctrl+a":
			if len(m.filtered) == 0 {
				m.status = "Selected: 0"
				return m, nil
			}
			for _, c := range m.filtered {
				m.selectedAliases[c.host.Alias] = struct{}{}
			}
			m.status = fmt.Sprintf("Selected: %d", len(m.selectedAliases))
			return m, nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.recompute()
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.quitting = true
		return m, tea.Quit
	case "/":
		m.input.Focus()
		return m, nil
	case "up", "k":
		m.move(-1)
		return m, nil
	case "down", "j":
		m.move(1)
		return m, nil
	case "ctrl+u":
		m.move(-(m.listHeight() / 2))
		return m, nil
	case "ctrl+d":
		m.move(m.listHeight() / 2)
		return m, nil
	case "g":
		if m.pendingG {
			m.selected = 0
			m.scroll = 0
			m.pendingG = false
			return m, nil
		}
		m.pendingG = true
		return m, nil
	case "G":
		if len(m.filtered) > 0 {
			m.selected = len(m.filtered) - 1
			m.ensureVisible()
		}
		m.pendingG = false
		return m, nil
	case " ":
		if current := m.current(); current != nil {
			alias := current.host.Alias
			if _, ok := m.selectedAliases[alias]; ok {
				delete(m.selectedAliases, alias)
			} else {
				m.selectedAliases[alias] = struct{}{}
			}
		}
		m.pendingG = false
		return m, nil
	case "f":
		if current := m.current(); current != nil {
			on := m.app.State.ToggleFavorite(current.host.Alias)
			_ = state.Save(m.app.StatePath, m.app.State)
			if on {
				m.status = "favorite added"
			} else {
				m.status = "favorite removed"
			}
			m.recompute()
		}
		m.pendingG = false
		return m, nil
	case "F":
		m.filterFavorites = !m.filterFavorites
		if m.filterFavorites {
			m.filterRecents = false
		}
		m.recompute()
		m.pendingG = false
		return m, nil
	case "R":
		m.filterRecents = !m.filterRecents
		if m.filterRecents {
			m.filterFavorites = false
		}
		m.recompute()
		m.pendingG = false
		return m, nil
	case "ctrl+a":
		if len(m.filtered) == 0 {
			m.status = "Selected: 0"
			return m, nil
		}
		for _, c := range m.filtered {
			m.selectedAliases[c.host.Alias] = struct{}{}
		}
		m.status = fmt.Sprintf("Selected: %d", len(m.selectedAliases))
		m.pendingG = false
		return m, nil
	case "a":
		m.showAddHost = true
		m.add.field = 0
		m.add.status = ""
		m.resetAddHostFields()
		m.focusAddField()
		m.pendingG = false
		return m, nil
	case "c":
		m.pendingG = false
		return m.openCredentialEditor("set")
	case "d":
		m.pendingG = false
		return m.openCredentialEditor("delete")
	case "enter":
		m.pendingG = false
		return m.enterDefault()
	case "v":
		m.pendingG = false
		return m.runMulti(m.app.SplitVert, "opened vertical splits")
	case "s":
		m.pendingG = false
		return m.runMulti(m.app.SplitHoriz, "opened horizontal splits")
	case "w":
		m.pendingG = false
		return m.runMulti(m.app.NewWindow, "opened tmux windows")
	case "t":
		m.pendingG = false
		return m.runTiled()
	case "p":
		m.pendingG = false
		current := m.current()
		if current == nil {
			return m, nil
		}
		m.app.State.AddRecent(current.host.Alias)
		_ = state.Save(m.app.StatePath, m.app.State)
		m.enableLogging(current.host.Alias)
		m.execAfterExit = m.app.Connect(current.host.Alias)
		m.quitting = true
		return m, tea.Quit
	default:
		m.pendingG = false
		return m, nil
	}
}

func (m model) openCredentialEditor(action string) (tea.Model, tea.Cmd) {
	current := m.current()
	if current == nil {
		return m, nil
	}
	m.showCredential = true
	m.credential.action = action
	m.credential.host = current.host.Alias
	m.credential.status = ""
	m.credential.field = 0
	m.credential.user.SetValue(current.host.User)
	m.credential.kind.SetValue("password")
	m.focusCredentialField()
	return m, nil
}

func (m model) handleAddHost(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.showAddHost = false
		m.add.status = ""
		return m, nil
	case "tab", "down", "j":
		m.add.field = (m.add.field + 1) % 6
		m.focusAddField()
		return m, nil
	case "shift+tab", "up", "k":
		m.add.field = (m.add.field + 5) % 6
		m.focusAddField()
		return m, nil
	case "enter":
		input, err := m.addInput()
		if err != nil {
			m.add.status = err.Error()
			return m, nil
		}
		if err := m.app.AddHost(input); err != nil {
			m.add.status = err.Error()
			return m, nil
		}
		hosts, err := sshconfig.LoadDefault()
		if err != nil {
			m.add.status = err.Error()
			return m, nil
		}
		m.candidates = buildCandidates(hosts)
		m.recompute()
		m.showAddHost = false
		m.status = "host added to ~/.ssh/config"
		m.resetAddHostFields()
		return m, nil
	}

	var cmd tea.Cmd
	switch m.add.field {
	case 0:
		m.add.alias, cmd = m.add.alias.Update(msg)
	case 1:
		m.add.hostName, cmd = m.add.hostName.Update(msg)
	case 2:
		m.add.user, cmd = m.add.user.Update(msg)
	case 3:
		m.add.port, cmd = m.add.port.Update(msg)
	case 4:
		m.add.proxyJump, cmd = m.add.proxyJump.Update(msg)
	case 5:
		m.add.identityFile, cmd = m.add.identityFile.Update(msg)
	}
	return m, cmd
}

func (m model) handleCredential(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.showCredential = false
		m.credential.status = ""
		return m, nil
	case "tab", "down", "j":
		m.credential.field = (m.credential.field + 1) % 2
		m.focusCredentialField()
		return m, nil
	case "shift+tab", "up", "k":
		m.credential.field = (m.credential.field + 1) % 2
		m.focusCredentialField()
		return m, nil
	case "enter":
		if m.app.ExecCredential == nil {
			m.credential.status = "credential execution is not configured"
			return m, nil
		}
		user := strings.TrimSpace(m.credential.user.Value())
		kind := strings.TrimSpace(m.credential.kind.Value())
		if kind == "" {
			kind = "password"
		}
		cmd, err := m.app.ExecCredential(m.credential.action, m.credential.host, user, kind)
		if err != nil {
			m.credential.status = err.Error()
			return m, nil
		}
		m.showCredential = false
		m.execAfterExit = cmd
		m.quitting = true
		return m, tea.Quit
	}

	var cmd tea.Cmd
	switch m.credential.field {
	case 0:
		m.credential.user, cmd = m.credential.user.Update(msg)
	case 1:
		m.credential.kind, cmd = m.credential.kind.Update(msg)
	}
	return m, cmd
}

func (m *model) addInput() (sshconfig.AddHostInput, error) {
	port := 0
	if value := strings.TrimSpace(m.add.port.Value()); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return sshconfig.AddHostInput{}, fmt.Errorf("port must be a positive integer")
		}
		port = parsed
	}
	return sshconfig.AddHostInput{
		Alias:        strings.TrimSpace(m.add.alias.Value()),
		HostName:     strings.TrimSpace(m.add.hostName.Value()),
		User:         strings.TrimSpace(m.add.user.Value()),
		Port:         port,
		ProxyJump:    strings.TrimSpace(m.add.proxyJump.Value()),
		IdentityFile: strings.TrimSpace(m.add.identityFile.Value()),
	}, nil
}

func (m *model) focusAddField() {
	fields := []*textinput.Model{&m.add.alias, &m.add.hostName, &m.add.user, &m.add.port, &m.add.proxyJump, &m.add.identityFile}
	for index, field := range fields {
		if index == m.add.field {
			field.Focus()
		} else {
			field.Blur()
		}
	}
}

func (m *model) focusCredentialField() {
	fields := []*textinput.Model{&m.credential.user, &m.credential.kind}
	for index, field := range fields {
		if index == m.credential.field {
			field.Focus()
		} else {
			field.Blur()
		}
	}
}

func (m *model) resetAddHostFields() {
	m.add.alias.SetValue("")
	m.add.hostName.SetValue("")
	m.add.user.SetValue("")
	m.add.port.SetValue("")
	m.add.proxyJump.SetValue("")
	m.add.identityFile.SetValue("")
}

func (m model) enterDefault() (tea.Model, tea.Cmd) {
	// Multi-selected: use enter mode for tmux actions, fall back to windows for "p".
	if len(m.selectedAliases) > 0 {
		switch m.app.EnterMode {
		case "v":
			return m.runMulti(m.app.SplitVert, "opened vertical splits")
		case "s":
			return m.runMulti(m.app.SplitHoriz, "opened horizontal splits")
		default:
			return m.runMulti(m.app.NewWindow, "opened tmux windows")
		}
	}
	// Single host: dispatch based on enter mode.
	current := m.current()
	if current == nil {
		return m, nil
	}
	m.app.State.AddRecent(current.host.Alias)
	_ = state.Save(m.app.StatePath, m.app.State)
	switch m.app.EnterMode {
	case "w":
		return m.runMulti(m.app.NewWindow, "opened tmux window")
	case "v":
		return m.runMulti(m.app.SplitVert, "opened vertical split")
	case "s":
		return m.runMulti(m.app.SplitHoriz, "opened horizontal split")
	default:
		m.enableLogging(current.host.Alias)
		cmd := m.app.Connect(current.host.Alias)
		m.execAfterExit = cmd
		m.quitting = true
		return m, tea.Quit
	}
}

func (m model) enableLogging(alias string) {
	if m.app.SetupLogging != nil {
		m.app.SetupLogging(alias)
	}
}

func (m model) runTiled() (tea.Model, tea.Cmd) {
	targets := m.targets()
	if len(targets) == 0 {
		return m, nil
	}
	for _, alias := range targets {
		m.app.State.AddRecent(alias)
	}
	_ = state.Save(m.app.StatePath, m.app.State)
	// Single host: fall back to new window.
	if len(targets) == 1 {
		return m.runMulti(m.app.NewWindow, "opened tmux window")
	}
	return m, m.runAction(func() error {
		if !m.app.InTmux() {
			return fmt.Errorf("tiled layout requires running inside tmux")
		}
		if m.app.Tiled == nil {
			return fmt.Errorf("tiled layout not available")
		}
		return m.app.Tiled(targets, "tiled")
	}, true, "opened tiled layout")
}

func (m model) runMulti(action func(string) error, statusText string) (tea.Model, tea.Cmd) {
	targets := m.targets()
	if len(targets) == 0 {
		return m, nil
	}
	for _, alias := range targets {
		m.app.State.AddRecent(alias)
	}
	_ = state.Save(m.app.StatePath, m.app.State)
	return m, m.runAction(func() error {
		if !m.app.InTmux() {
			return fmt.Errorf("tmux actions require running inside tmux")
		}
		for _, alias := range targets {
			if err := action(alias); err != nil {
				return err
			}
		}
		return nil
	}, true, statusText)
}

func (m model) runAction(action func() error, quit bool, success string) tea.Cmd {
	return func() tea.Msg {
		if err := action(); err != nil {
			return errMsg{err: err}
		}
		if quit {
			return tea.Quit()
		}
		return actionMsg{text: success}
	}
}

func (m *model) recompute() {
	query := strings.ToLower(strings.TrimSpace(m.input.Value()))
	out := make([]candidate, 0, len(m.candidates))
	for _, candidate := range m.candidates {
		if m.filterFavorites && !m.app.State.IsFavorite(candidate.host.Alias) {
			continue
		}
		if m.filterRecents && !contains(m.app.State.Recents, candidate.host.Alias) {
			continue
		}
		if query == "" || fuzzyMatch(query, candidate.search) {
			out = append(out, candidate)
		}
	}
	m.filtered = out
	if m.selected >= len(m.filtered) {
		m.selected = len(m.filtered) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
	m.ensureVisible()
}

func fuzzyMatch(query, text string) bool {
	if query == "" {
		return true
	}
	queryRunes := []rune(query)
	index := 0
	for _, r := range []rune(text) {
		if index < len(queryRunes) && r == queryRunes[index] {
			index++
		}
	}
	return index == len(queryRunes)
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func (m *model) move(delta int) {
	if len(m.filtered) == 0 {
		return
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(m.filtered) {
		m.selected = len(m.filtered) - 1
	}
	m.ensureVisible()
}

func (m *model) ensureVisible() {
	height := m.listHeight()
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	if m.selected >= m.scroll+height {
		m.scroll = m.selected - height + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m model) listHeight() int {
	if m.height <= 0 {
		return 12
	}
	height := m.height - 8
	if height < 5 {
		return 5
	}
	return height
}

func (m model) current() *candidate {
	if len(m.filtered) == 0 || m.selected < 0 || m.selected >= len(m.filtered) {
		return nil
	}
	return &m.filtered[m.selected]
}

func (m model) targets() []string {
	if len(m.selectedAliases) == 0 {
		if current := m.current(); current != nil {
			return []string{current.host.Alias}
		}
		return nil
	}
	items := make([]string, 0, len(m.selectedAliases))
	for _, candidate := range m.filtered {
		if _, ok := m.selectedAliases[candidate.host.Alias]; ok {
			items = append(items, candidate.host.Alias)
		}
	}
	if len(items) == 0 {
		for alias := range m.selectedAliases {
			items = append(items, alias)
		}
	}
	return items
}

func (m model) View() string {
	if m.showAddHost {
		return m.viewAddHost()
	}
	if m.showCredential {
		return m.viewCredential()
	}
	var builder strings.Builder
	builder.WriteString("tmux-ssh-manager\n")
	builder.WriteString(m.input.View())
	builder.WriteString("\n\n")

	height := m.listHeight()
	end := min(len(m.filtered), m.scroll+height)
	for index := m.scroll; index < end; index++ {
		candidate := m.filtered[index]
		prefix := "  "
		if index == m.selected {
			prefix = "> "
		}
		selection := " "
		if _, ok := m.selectedAliases[candidate.host.Alias]; ok {
			selection = "x"
		}
		star := " "
		if m.app.State.IsFavorite(candidate.host.Alias) {
			star = m.favoriteStyle.Render("★")
		}
		line := fmt.Sprintf("%s[%s] %s %s", prefix, selection, star, candidate.line)
		if index == m.selected {
			line = m.selectedStyle.Render(line)
		}
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	if len(m.filtered) == 0 {
		builder.WriteString(m.dimStyle.Render("no hosts matched the current filter"))
		builder.WriteByte('\n')
	}
	builder.WriteByte('\n')
	builder.WriteString(m.helpStyle.Render("/ search • enter connect • space select • v split-v • s split-h • w window • t tiled • c store cred • d delete cred • f favorite • F favorites • R recents • a add host • q quit"))
	builder.WriteByte('\n')
	if m.status != "" {
		builder.WriteString(m.statusStyle.Render(m.status))
		builder.WriteByte('\n')
	}
	return builder.String()
}

func (m model) viewCredential() string {
	actionText := "Store"
	if m.credential.action == "delete" {
		actionText = "Delete"
	}
	parts := []string{
		actionText + " Credential",
		"",
		"Host: " + m.credential.host,
		m.credential.user.View(),
		m.credential.kind.View(),
		"",
		m.helpStyle.Render("enter run • tab/j/k move • esc cancel"),
	}
	if m.credential.status != "" {
		parts = append(parts, m.statusStyle.Render(m.credential.status))
	}
	return strings.Join(parts, "\n")
}

func (m model) viewAddHost() string {
	parts := []string{
		"Add SSH Host",
		"",
		m.add.alias.View(),
		m.add.hostName.View(),
		m.add.user.View(),
		m.add.port.View(),
		m.add.proxyJump.View(),
		m.add.identityFile.View(),
		"",
		m.helpStyle.Render("enter save • tab/j/k move • esc cancel"),
	}
	if m.add.status != "" {
		parts = append(parts, m.statusStyle.Render(m.add.status))
	}
	return strings.Join(parts, "\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// disableTermQueries best-effort disables terminal capability probing.
//
// Some terminals respond to OSC/DSR queries (e.g. OSC 11 background-color, DSR
// cursor position) by writing escape sequences back to the app's stdin. If those
// responses escape the TUI lifecycle, the next exec'd program (ssh) or your
// local shell can interpret them as literal input.
func disableTermQueries() func() {
	// Lipgloss uses termenv for terminal capability detection.
	//
	// Some detection paths can send OSC/DSR queries (like OSC 11), which can
	// cause certain terminals to write replies back onto stdin. If those replies
	// outlive the TUI, they can leak into the next program as literal input.
	//
	// Force a fixed profile for the duration of the picker.
	prevOut := termenv.DefaultOutput()
	prevProfile := lipgloss.ColorProfile()

	forced := termenv.NewOutput(os.Stdout, termenv.WithProfile(termenv.ANSI256), termenv.WithTTY(true))
	termenv.SetDefaultOutput(forced)
	lipgloss.SetColorProfile(termenv.ANSI256)

	return func() {
		termenv.SetDefaultOutput(prevOut)
		lipgloss.SetColorProfile(prevProfile)
	}
}
