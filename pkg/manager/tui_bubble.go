package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"tmux-ssh-manager/pkg/sessionfmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func RunTUI(cfg *Config, opts UIOptions) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 20
	}

	if os.Getenv("TMUX_SSH_MANAGER_IN_POPUP") != "" {
		_ = os.Setenv("TERM", "xterm-256color")
		opts.ExecReplace = false
	}

	m := newModel(cfg, opts)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type netNode struct {
	ID            string
	DisplayName   string
	Configured    bool
	Downregulated bool
	IdentityHint  string
	Resolved      *ResolvedHost
}

type netEdge struct {
	FromID     string
	ToID       string
	LocalPort  string
	RemotePort string
}

type netLLDPProgressMsg struct {
	Status string
}

type netLLDPDoneMsg struct {
	Status   string
	Err      string
	Targets  []ResolvedHost
	Results  []LLDPParseResult
	Failures map[string]string // host -> error
}

type statusMsg string

type errMsg struct {
	Err error
}

type model struct {
	cfg        *Config
	opts       UIOptions
	input      textinput.Model
	candidates []candidate
	filtered   []candidate

	selected        int
	scroll          int
	showHelp        bool
	showDashBrowser bool
	dashSelected    int
	dashLayoutMode  int // 0=use dashboard layout; 1=tiled; 2=even-horizontal; 3=even-vertical; 4=main-vertical; 5=main-horizontal

	recording            bool
	recordingName        string
	recordingDescription string
	recordedPanes        map[string]*RecordedPane // key: pane_id ("%0"), value holds Host + Commands

	paneHost map[string]string // key: pane_id ("%0") -> hostKey (Host.Name)

	// Host Settings overlay (SecureCRT-like "session properties")
	showHostSettings bool
	hostSettingsSel  int

	// Router-ID editor modal (stored in HostExtras.router_id for topology matching)
	showRouterIDEditor bool
	routerIDInput      textinput.Model

	// Mgmt IP editor modal (stored in HostExtras.mgmt_ip for topology matching)
	showMgmtIPEditor bool
	mgmtIPInput      textinput.Model

	// Device OS editor modal (stored in HostExtras.device_os for topology discovery)
	showDeviceOSEditor bool
	deviceOSInput      textinput.Model

	// --- Network view (LLDP topology) ---
	showNetworkView bool
	netLoading      bool
	netStatus       string
	netErr          string
	netSelected     []ResolvedHost // snapshot of selected/current at time of launch

	// Network view render options
	netViewMode      string // "layered" (default) | "edges" | "list"
	netUseBoxDrawing bool   // if true, use Unicode box-drawing; else ASCII-only

	// netNodes/netEdges are rendered in ASCII/box drawing.
	// Nodes can be configured hosts or discovered unknown nodes.
	netNodes      []netNode
	netEdges      []netEdge
	netNodeIndex  map[string]int // id -> index in netNodes
	netSelectedID string         // focused node id
	netQuery      textinput.Model
	netQueryMode  bool

	// When true, the node is de-emphasized (unknown nodes OR configured but “island/no data” nodes)
	// so the view doesn’t overwhelm with “everything is primary”.
	// (This is a rendering property on netNode; included here for clarity of behavior.)

	// SSH config duplicates (primary ~/.ssh/config only)
	dupAliasCount map[string]int // alias -> number of Host blocks in ~/.ssh/config

	// Merge-duplicates confirmation modal (for ~/.ssh/config primary only)
	showMergeDupsConfirm bool
	mergeDupsAlias       string
	mergeDupsCount       int
	mergeDupsConfirmHint string

	// Add SSH Host modal (writes to primary ~/.ssh/config)
	showAddSSHHost     bool
	addSSHFieldSel     int
	addSSHAlias        textinput.Model
	addSSHHostName     textinput.Model
	addSSHUser         textinput.Model
	addSSHPort         textinput.Model
	addSSHProxyJump    textinput.Model
	addSSHIdentityFile textinput.Model
	addSSHForwardAgent bool // default true

	// --- SSH config import/export selection modal ---
	// Provides a menu to select literal Host aliases from ~/.ssh/config for:
	// - Export: write a new ssh config file containing only the selected hosts
	// - Import: merge/update-by-name into tmux-ssh-manager YAML config (hosts.yaml)
	showSSHConfigXfer     bool
	sshXferMode           string // "export" | "import"
	sshXferEntries        []SSHHostEntry
	sshXferFilteredIdx    []int            // indices into sshXferEntries (after xfer query filter)
	sshXferSelectedSet    map[int]struct{} // selected indices into sshXferEntries
	sshXferSelectedCursor int
	sshXferScroll         int
	sshXferQuery          textinput.Model
	sshXferStatus         string

	// Path prompt modal (used for both ssh export and ssh import)
	showSSHPathPrompt bool
	sshPathMode       string // "export" | "import"
	sshPathInput      textinput.Model
	sshPathHint       string

	// After import/export, offer to open an editor to review the written file.
	// This mirrors the UX used elsewhere (e.g. editing ~/.ssh/config via Ctrl+E in Add Host).
	showSSHPostWriteConfirm bool
	sshPostWritePath        string
	sshPostWriteAction      string // "export" | "import"

	// Add SSH Host modal "command mode" (vim-ish):
	// - default is insert mode (cmd mode OFF): digits/0/g/G are inserted into fields
	// - when cmd mode ON: 0/g/G and numeric jumps are navigation shortcuts
	addSSHCmdMode bool

	status string

	// --- Logs viewer state ---
	showLogs      bool
	logHostKey    string
	logFilePath   string
	logFiles      []string
	logSelected   int
	logStartLine  int
	logLines      []string
	logTotalLines int

	// view state
	width    int
	height   int
	ready    bool
	quitting bool

	// vim helpers
	pendingG bool

	// Search direction for n/N (vim-like)
	// - true: forward (n goes down)
	// - false: backward (n goes up)
	searchForward bool

	// ":" command-line mode (SecureCRT-like command bar)
	showCmdline bool
	cmdline     textinput.Model

	// Command bar helpers
	cmdCandidates []string
	cmdSuggestIdx int

	// numeric quick-select buffer (e.g., "15" then Enter)
	numBuf string

	// ephemeral status timer
	statusUntil time.Time

	// favorites/recents and selection/modes
	favorites       map[string]struct{}
	recents         []string
	filterFavorites bool
	filterRecents   bool
	selectedSet     map[int]struct{}

	// tmux targets created during this session (to close on exit)
	createdPaneIDs   []string
	createdWindowIDs []string

	// persistence
	statePath string
	state     *State
	theme     Theme
}

func newModel(cfg *Config, opts UIOptions) model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "search..."
	ti.CharLimit = 256
	ti.Cursor.Style = ti.Cursor.Style.Bold(true)
	ti.SetValue(strings.TrimSpace(opts.InitialQuery))
	ti.PromptStyle = ti.PromptStyle.Bold(true)
	// Start in "insert" mode by default: search is focused so typing immediately filters.
	ti.Focus()

	// ":" command bar (SecureCRT-like). Kept separate from search input.
	ci := textinput.New()
	ci.Prompt = ":"
	ci.Placeholder = "menu"
	ci.CharLimit = 256
	ci.Cursor.Style = ci.Cursor.Style.Bold(true)
	ci.PromptStyle = ci.PromptStyle.Bold(true)

	// Add SSH Host form inputs (primary ~/.ssh/config)
	ai := textinput.New()
	ai.Prompt = "Alias: "
	ai.Placeholder = "e.g. narrs-dev1.lmig.com"
	ai.CharLimit = 256

	hni := textinput.New()
	hni.Prompt = "HostName: "
	hni.Placeholder = "default: same as Alias"
	hni.CharLimit = 256

	ui := textinput.New()
	ui.Prompt = "User: "
	ui.Placeholder = "optional"
	ui.CharLimit = 128

	porti := textinput.New()
	porti.Prompt = "Port: "
	porti.Placeholder = "optional (default 22)"
	porti.CharLimit = 16

	pji := textinput.New()
	pji.Prompt = "ProxyJump: "
	pji.Placeholder = "optional (bastion or jump host)"
	pji.CharLimit = 256

	idi := textinput.New()
	idi.Prompt = "IdentityFile: "
	idi.Placeholder = "optional (e.g. ~/.ssh/id_rsa)"
	idi.CharLimit = 512

	// Router-ID editor input (HostExtras.router_id)
	ri := textinput.New()
	ri.Prompt = "Router-ID: "
	ri.CharLimit = 128

	// Mgmt IP editor input (HostExtras.mgmt_ip)
	mi := textinput.New()
	mi.Prompt = "Mgmt IP: "
	mi.CharLimit = 128

	// Device OS editor input (HostExtras.device_os)
	doi := textinput.New()
	doi.Prompt = "Device OS: "
	doi.CharLimit = 64

	// Network view quick-search input (within topology view)
	nqi := textinput.New()
	nqi.Prompt = "find: "
	nqi.Placeholder = "name or ip..."
	nqi.CharLimit = 256

	// SSH config xfer quick-search input (within import/export modal)
	xqi := textinput.New()
	xqi.Prompt = "ssh: "
	xqi.Placeholder = "filter hosts..."
	xqi.CharLimit = 256

	// SSH import/export path prompt input (absolute or relative to cwd)
	pi := textinput.New()
	pi.Prompt = "path: "
	pi.Placeholder = "absolute or relative (cwd)"
	pi.CharLimit = 1024

	cands := buildCandidates(cfg)
	m := model{
		cfg:             cfg,
		opts:            opts,
		input:           ti,
		candidates:      cands,
		filtered:        rankMatches(cands, ti.Value()),
		selected:        0,
		scroll:          0,
		showHelp:        false,
		searchForward:   true,
		showCmdline:     false,
		cmdline:         ci,
		cmdCandidates:   nil,
		cmdSuggestIdx:   -1,
		showLogs:        false,
		logSelected:     0,
		logStartLine:    0,
		logLines:        nil,
		logTotalLines:   0,
		favorites:       make(map[string]struct{}),
		recents:         []string{},
		filterFavorites: false,
		filterRecents:   false,
		selectedSet:     make(map[int]struct{}),

		// network view defaults
		showNetworkView:  false,
		netLoading:       false,
		netStatus:        "",
		netErr:           "",
		netSelected:      nil,
		netViewMode:      "layered",
		netUseBoxDrawing: strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_NET_ASCII")) == "",
		netNodes:         nil,
		netEdges:         nil,
		netNodeIndex:     make(map[string]int),
		netSelectedID:    "",
		netQuery:         nqi,
		netQueryMode:     false,

		// SSH config duplicate map
		dupAliasCount: make(map[string]int),

		// SSH config import/export modal defaults
		showSSHConfigXfer:     false,
		sshXferMode:           "",
		sshXferEntries:        nil,
		sshXferFilteredIdx:    nil,
		sshXferSelectedSet:    make(map[int]struct{}),
		sshXferSelectedCursor: 0,
		sshXferScroll:         0,
		sshXferQuery:          xqi,
		sshXferStatus:         "",

		// SSH path prompt defaults
		showSSHPathPrompt: false,
		sshPathMode:       "",
		sshPathInput:      pi,
		sshPathHint:       "",

		// Post-write confirm defaults
		showSSHPostWriteConfirm: false,
		sshPostWritePath:        "",
		sshPostWriteAction:      "",

		// Add SSH Host modal defaults
		showAddSSHHost:     false,
		addSSHFieldSel:     0,
		addSSHAlias:        ai,
		addSSHHostName:     hni,
		addSSHUser:         ui,
		addSSHPort:         porti,
		addSSHProxyJump:    pji,
		addSSHIdentityFile: idi,
		addSSHForwardAgent: true,
		addSSHCmdMode:      false,

		// recorder defaults
		recording:            false,
		recordingName:        "",
		recordingDescription: "",
		recordedPanes:        make(map[string]*RecordedPane),

		// router-id editor defaults
		showRouterIDEditor: false,
		routerIDInput:      ri,

		// mgmt-ip editor defaults
		showMgmtIPEditor: false,
		mgmtIPInput:      mi,

		// device-os editor defaults
		showDeviceOSEditor: false,
		deviceOSInput:      doi,

		// pane mapping defaults
		paneHost: make(map[string]string),
	}
	// Load persistent favorites/recents state
	if path, err := DefaultStatePath(); err == nil {
		m.statePath = path
	}
	if st, err := LoadState(m.statePath); err == nil && st != nil {
		m.state = st
		if len(st.Favorites) > 0 {
			for _, n := range st.Favorites {
				n = strings.TrimSpace(n)
				if n != "" {
					m.favorites[n] = struct{}{}
				}
			}
		}
		if len(st.Recents) > 0 {
			m.recents = append([]string(nil), st.Recents...)
		}
	}
	// Best-effort: compute duplicates in primary ~/.ssh/config (for marker + merge hotkey).
	// This is intentionally non-fatal (the TUI should still work even if ~/.ssh/config is unreadable).
	if rep, err := ComputePrimaryDuplicateAliases(); err == nil && rep != nil && rep.AliasToBlocks != nil {
		for alias, blocks := range rep.AliasToBlocks {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			m.dupAliasCount[alias] = len(blocks)
		}
	}

	m.theme = LoadTheme("")
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tea.EnterAltScreen, textinput.Blink)
}

func (m *model) buildCmdCandidates() []string {
	// Keep this list small, memorable, and discoverable (SecureCRT-like "command bar").
	// Commands are space-delimited; most support abbreviations.
	cands := []string{
		"menu",
		"help",
		"h",
		"q",
		"quit",
		"exit",

		"search ",
		"/ ",
		"? ",
		"clear",
		"all",
		"fav",
		"favorites",
		"recent",
		"recents",

		"dash",
		"dash ",
		"dash save ",
		"dash export ",
		"dash apply ",
		"dash apply-file ",
		"dashboard",
		"dashboards",

		// SSH config import/export (OpenSSH ~/.ssh/config)
		// - :ssh export -> select hosts from ~/.ssh/config and export them
		// - :ssh import -> select hosts from ~/.ssh/config and import them
		"ssh",
		"ssh ",
		"ssh export",
		"ssh export ",
		"ssh import",
		"ssh import ",

		// Send commands (records cleanly + works for NOC dashboards).
		"send ",
		"sendall ",

		// Watch helpers (sugar over :send/:sendall).
		// Usage:
		//   :watch <interval_s> <cmd...>
		//   :watchall <interval_s> <cmd...>
		// interval_s is optional; defaults to 2.
		"watch ",
		"watchall ",

		// Recorder (first-pass): captures commands tmux-ssh-manager sends into panes (not live keystrokes in SSH).
		"record start ",
		"record stop",
		"record status",
		"record save ",
		"record delete ",

		"connect",
		"c",
		"split v",
		"split h",
		"window",
		"w",
		"windows",

		"logs",
		"log toggle",
		"log on",
		"log off",

		"login",
		"login status",
		"login askpass",
		"login manual",

		"cred status",
		"cred set",
		"cred delete",

		"run ",
	}
	// Add macro names and dashboard names for discoverability.
	if m.cfg != nil {
		for _, mac := range m.cfg.Macros {
			name := strings.TrimSpace(mac.Name)
			if name != "" {
				cands = append(cands, "run "+name)
			}
		}
		for _, d := range m.cfg.Dashboards {
			name := strings.TrimSpace(d.Name)
			if name != "" {
				cands = append(cands, "dash "+name)
			}
		}
	}
	// Add recorded dashboard names for discoverability (state.json).
	if m.state != nil && len(m.state.RecordedDashboards) > 0 {
		for _, rd := range m.state.RecordedDashboards {
			name := strings.TrimSpace(rd.Name)
			if name != "" {
				cands = append(cands, "dash "+name)
			}
		}
	}
	sort.Strings(cands)
	return cands
}

func (m *model) cmdSuggestions(prefix string) []string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return nil
	}
	out := make([]string, 0, 10)
	for _, c := range m.cmdCandidates {
		if strings.HasPrefix(strings.ToLower(c), prefix) {
			out = append(out, c)
			if len(out) >= 10 {
				break
			}
		}
	}
	return out
}

func (m *model) currentOrSelectedResolved() []ResolvedHost {
	targets := m.selectedResolved()
	if len(targets) == 0 {
		if sel := m.current(); sel != nil {
			targets = []ResolvedHost{sel.Resolved}
		}
	}
	return targets
}

func (m *model) findMacro(name string) *Macro {
	if m.cfg == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	for i := range m.cfg.Macros {
		if m.cfg.Macros[i].Name == name {
			return &m.cfg.Macros[i]
		}
	}
	return nil
}

// --- Update ---

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		return m, nil

	case tea.SuspendMsg:
		// No-op: we no longer use Bubble Tea suspend for external editor launching.
		return m, nil

	case statusMsg:
		m.setStatus(string(msg), 2500)
		return m, nil

	case errMsg:
		if msg.Err != nil {
			m.setStatus(msg.Err.Error(), 4000)
		} else {
			m.setStatus("error", 2500)
		}
		return m, nil

	case netLLDPProgressMsg:
		m.netLoading = true
		m.netStatus = msg.Status
		return m, nil

	case netLLDPDoneMsg:
		m.netLoading = false
		m.netErr = msg.Err
		m.netStatus = msg.Status
		m.netSelected = msg.Targets

		// Build graph with “configured hosts are first-class”, unknown nodes downregulated,
		// and configured-but-no-LLDP/island nodes also downregulated.
		m.netNodes, m.netEdges, m.netNodeIndex = buildNetGraph(m.cfg, msg.Targets, msg.Results, msg.Failures)

		// Focus: prefer first configured node, else any.
		m.netSelectedID = ""
		for _, n := range m.netNodes {
			if n.Configured {
				m.netSelectedID = n.ID
				break
			}
		}
		if m.netSelectedID == "" && len(m.netNodes) > 0 {
			m.netSelectedID = m.netNodes[0].ID
		}
		return m, tea.ClearScreen

	case tea.KeyMsg:
		if handled, quit := m.handleGlobalKeys(msg); handled {
			if quit {
				return m.quit()
			}
			return m, nil
		}

		// Merge duplicates confirmation modal has highest priority (modal).
		if m.showMergeDupsConfirm {
			switch msg.String() {
			case "esc", "n", "N", "q":
				m.showMergeDupsConfirm = false
				m.mergeDupsAlias = ""
				m.mergeDupsCount = 0
				m.mergeDupsConfirmHint = ""
				m.pendingG = false
				m.input.Blur()
				m.recomputeFilter()
				return m, tea.ClearScreen

			case "y", "Y":
				alias := strings.TrimSpace(m.mergeDupsAlias)
				if alias == "" {
					m.setStatus("merge: empty alias", 1500)
					m.showMergeDupsConfirm = false
					return m, tea.ClearScreen
				}

				changed, err := MergePrimaryDuplicateAlias(alias)
				if err != nil {
					m.setStatus(fmt.Sprintf("merge failed: %v", err), 4000)
					m.showMergeDupsConfirm = false
					return m, tea.ClearScreen
				}
				if !changed {
					m.setStatus("merge: nothing to do", 1500)
					m.showMergeDupsConfirm = false
					return m, tea.ClearScreen
				}

				removed := 0
				if m.mergeDupsCount > 1 {
					removed = m.mergeDupsCount - 1
				}
				m.setStatus(fmt.Sprintf("merged duplicates for %s (kept first, removed %d)", alias, removed), 3000)
				m.showMergeDupsConfirm = false
				m.mergeDupsAlias = ""
				m.mergeDupsCount = 0
				m.mergeDupsConfirmHint = ""

				// Refresh duplicate map (best-effort).
				if rep, err := ComputePrimaryDuplicateAliases(); err == nil && rep != nil && rep.AliasToBlocks != nil {
					m.dupAliasCount = make(map[string]int)
					for a, blocks := range rep.AliasToBlocks {
						a = strings.TrimSpace(a)
						if a == "" {
							continue
						}
						m.dupAliasCount[a] = len(blocks)
					}
				}

				// Rebuild config from SSH aliases so list reflects any last-wins changes immediately.
				if conf, err := LoadConfigFromSSH(); err == nil && conf != nil {
					m.cfg = conf
					m.candidates = buildCandidates(conf)
					m.recomputeFilter()
				}
				return m, tea.ClearScreen
			default:
				return m, nil
			}
		}

		// Router-ID editor modal: highest priority within normal UI (after global keys).
		if m.showRouterIDEditor {
			switch msg.String() {
			case "esc", "q":
				m.showRouterIDEditor = false
				m.pendingG = false
				m.numBuf = ""
				m.routerIDInput.Blur()
				m.setStatus("router-id: cancelled", 1200)
				return m, tea.ClearScreen

			case "enter":
				sel := m.current()
				if sel == nil {
					m.setStatus("router-id: no host selected", 1500)
					return m, nil
				}
				hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
				if hostKey == "" {
					m.setStatus("router-id: empty host key", 1500)
					return m, nil
				}

				val := strings.TrimSpace(m.routerIDInput.Value())
				// Validate: allow empty (clear); otherwise must parse as IP.
				if val != "" {
					if net.ParseIP(val) == nil {
						m.setStatus("router-id: invalid IP (expected IPv4 or IPv6 literal)", 3500)
						return m, nil
					}
				}

				ex, _ := LoadHostExtras(hostKey)
				ex.HostKey = hostKey
				ex.RouterID = val
				if err := SaveHostExtras(ex); err != nil {
					m.setStatus(fmt.Sprintf("router-id save failed: %v", err), 3500)
					return m, nil
				}

				m.showRouterIDEditor = false
				m.routerIDInput.Blur()
				if val == "" {
					m.setStatus("router-id: cleared", 2000)
				} else {
					m.setStatus(fmt.Sprintf("router-id: %s", val), 2500)
				}
				return m, tea.ClearScreen

			default:
				m.routerIDInput.Focus()
				var cmd tea.Cmd
				m.routerIDInput, cmd = m.routerIDInput.Update(msg)
				return m, cmd
			}
		}

		// Mgmt IP editor modal: highest priority within normal UI (after global keys).
		if m.showMgmtIPEditor {
			switch msg.String() {
			case "esc", "q":
				m.showMgmtIPEditor = false
				m.pendingG = false
				m.numBuf = ""
				m.mgmtIPInput.Blur()
				m.setStatus("mgmt ip: cancelled", 1200)
				return m, tea.ClearScreen

			case "enter":
				sel := m.current()
				if sel == nil {
					m.setStatus("mgmt ip: no host selected", 1500)
					return m, nil
				}
				hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
				if hostKey == "" {
					m.setStatus("mgmt ip: empty host key", 1500)
					return m, nil
				}

				val := strings.TrimSpace(m.mgmtIPInput.Value())
				// Validate: allow empty (clear); otherwise must parse as IP.
				if val != "" {
					if net.ParseIP(val) == nil {
						m.setStatus("mgmt ip: invalid IP (expected IPv4 or IPv6 literal)", 3500)
						return m, nil
					}
				}

				ex, _ := LoadHostExtras(hostKey)
				ex.HostKey = hostKey
				ex.MgmtIP = val
				if err := SaveHostExtras(ex); err != nil {
					m.setStatus(fmt.Sprintf("mgmt ip save failed: %v", err), 3500)
					return m, nil
				}

				m.showMgmtIPEditor = false
				m.mgmtIPInput.Blur()
				if val == "" {
					m.setStatus("mgmt ip: cleared", 2000)
				} else {
					m.setStatus(fmt.Sprintf("mgmt ip: %s", val), 2500)
				}
				return m, tea.ClearScreen

			default:
				m.mgmtIPInput.Focus()
				var cmd tea.Cmd
				m.mgmtIPInput, cmd = m.mgmtIPInput.Update(msg)
				return m, cmd
			}
		}

		// Network view consumes keys when active.
		if m.showNetworkView {
			switch msg.String() {
			case "esc", "q":
				m.showNetworkView = false
				m.netQueryMode = false
				m.netQuery.Blur()
				m.pendingG = false
				m.numBuf = ""
				return m, tea.ClearScreen

			case "m":
				// Cycle view modes: layered -> edges -> list -> layered
				switch strings.ToLower(strings.TrimSpace(m.netViewMode)) {
				case "", "layered":
					m.netViewMode = "edges"
				case "edges":
					m.netViewMode = "list"
				default:
					m.netViewMode = "layered"
				}
				return m, tea.ClearScreen

			case "b":
				// Toggle box drawing vs ASCII-only.
				m.netUseBoxDrawing = !m.netUseBoxDrawing
				return m, tea.ClearScreen

			case "/":
				m.netQueryMode = true
				m.netQuery.SetValue("")
				m.netQuery.Focus()
				return m, nil

			case "enter", "c":
				// connect to focused node (only if configured)
				if n := m.netFocusedNode(); n != nil && n.Configured && n.Resolved != nil {
					// Reuse existing logic; respect tmux/outside-tmux semantics.
					if strings.TrimSpace(os.Getenv("TMUX")) == "" {
						return m.connectOrQuit(*n.Resolved)
					}
					if _, err := m.tmuxNewWindow(*n.Resolved); err != nil {
						_, _ = m.tmuxSplitV(*n.Resolved)
					}
					m.addRecent(n.Resolved.Host.Name)
					m.saveState()
					return m.quit()
				}
				m.setStatus("network: selected node is not a configured host", 2500)
				return m, nil

			case "v":
				if n := m.netFocusedNode(); n != nil && n.Configured && n.Resolved != nil {
					if strings.TrimSpace(os.Getenv("TMUX")) == "" {
						m.setStatus("network: split requires tmux", 2500)
						return m, nil
					}
					if _, err := m.tmuxSplitH(*n.Resolved); err != nil {
						m.setStatus(fmt.Sprintf("network split v failed: %v", err), 3500)
						return m, nil
					}
					m.addRecent(n.Resolved.Host.Name)
					m.saveState()
					return m.quit()
				}
				m.setStatus("network: selected node is not a configured host", 2500)
				return m, nil

			case "s":
				if n := m.netFocusedNode(); n != nil && n.Configured && n.Resolved != nil {
					if strings.TrimSpace(os.Getenv("TMUX")) == "" {
						m.setStatus("network: split requires tmux", 2500)
						return m, nil
					}
					if _, err := m.tmuxSplitV(*n.Resolved); err != nil {
						m.setStatus(fmt.Sprintf("network split h failed: %v", err), 3500)
						return m, nil
					}
					m.addRecent(n.Resolved.Host.Name)
					m.saveState()
					return m.quit()
				}
				m.setStatus("network: selected node is not a configured host", 2500)
				return m, nil

			case "r":
				// refresh using the same targets snapshot
				if len(m.netSelected) == 0 {
					m.setStatus("network: no targets", 1500)
					return m, nil
				}
				m.netErr = ""
				m.netStatus = "refreshing..."
				m.netLoading = true
				return m, runLLDPCollectionCmd(m.cfg, m.netSelected)

			case "tab", "right", "l":
				m.netFocusNext()
				return m, nil
			case "shift+tab", "left", "h":
				m.netFocusPrev()
				return m, nil
			case "j", "down":
				m.netFocusNext()
				return m, nil
			case "k", "up":
				m.netFocusPrev()
				return m, nil
			default:
				// query mode
				if m.netQueryMode {
					switch msg.String() {
					case "esc":
						m.netQueryMode = false
						m.netQuery.Blur()
						return m, nil
					case "enter":
						q := strings.TrimSpace(m.netQuery.Value())
						m.netQueryMode = false
						m.netQuery.Blur()
						if q != "" {
							m.netFocusByQuery(q)
						}
						return m, nil
					default:
						var cmd tea.Cmd
						m.netQuery, cmd = m.netQuery.Update(msg)
						return m, cmd
					}
				}
			}
		}

		// Post-write confirm modal: offer to open editor to review the written file.
		if m.showSSHPostWriteConfirm {
			switch msg.String() {
			case "q", "esc":
				m.showSSHPostWriteConfirm = false
				m.sshPostWritePath = ""
				m.sshPostWriteAction = ""
				return m, tea.ClearScreen
			case "enter", "y":
				// Open the written file in vi, mirroring the Ctrl+E editor mechanism used elsewhere.
				path := strings.TrimSpace(m.sshPostWritePath)
				if path == "" {
					m.setStatus("ssh: nothing to edit (empty path)", 2500)
					m.showSSHPostWriteConfirm = false
					return m, tea.ClearScreen
				}

				// Close confirm modal before launching editor.
				m.showSSHPostWriteConfirm = false
				m.sshPostWritePath = ""
				m.sshPostWriteAction = ""

				// Hardcode a safe terminal editor.
				editor := "vi"

				// Best-effort: restore terminal state before launching the editor.
				restoreTerminalForExec()

				if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_IN_POPUP")) == "1" {
					cmd := exec.Command(editor, path)
					cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
					m.setStatus("ssh: opening vi...", 1500)
					return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
						if err != nil {
							return errMsg{Err: fmt.Errorf("edit ssh file: %w", err)}
						}
						return statusMsg("ssh: editor closed")
					})
				}

				cmdline := fmt.Sprintf("exec %s %q", editor, path)
				cmd := exec.Command("tmux", "split-window", "-v", "-c", "#{pane_current_path}", "bash", "-lc", cmdline)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				if err := cmd.Run(); err != nil {
					m.setStatus(fmt.Sprintf("edit ssh file: %v", err), 3500)
					return m, tea.ClearScreen
				}
				m.setStatus("ssh: editor closed", 1500)
				return m, tea.ClearScreen
			default:
				return m, nil
			}
		}

		// SSH path prompt modal (absolute or relative path).
		if m.showSSHPathPrompt {
			switch msg.String() {
			case "q", "esc":
				m.showSSHPathPrompt = false
				m.sshPathMode = ""
				m.sshPathInput.SetValue("")
				m.sshPathInput.Blur()
				m.sshPathHint = ""
				m.setStatus("ssh: path prompt cancelled", 1200)
				return m, tea.ClearScreen

			case "enter":
				// Confirm path and proceed to the write/apply action.
				// Path can be absolute or relative to cwd.
				p := strings.TrimSpace(m.sshPathInput.Value())
				if p == "" {
					m.setStatus("ssh: path is required", 2500)
					return m, nil
				}

				mode := strings.ToLower(strings.TrimSpace(m.sshPathMode))
				m.showSSHPathPrompt = false
				m.sshPathMode = ""
				m.sshPathInput.Blur()
				m.sshPathHint = ""

				// Resolve to absolute if relative (cwd).
				if !filepath.IsAbs(p) {
					if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
						p = filepath.Join(cwd, p)
					}
				}

				// Build alias set from selected indices (from xfer modal selection).
				aliasSet := make(map[string]struct{}, len(m.sshXferSelectedSet))
				for idx := range m.sshXferSelectedSet {
					if idx < 0 || idx >= len(m.sshXferEntries) {
						continue
					}
					a := strings.TrimSpace(m.sshXferEntries[idx].Alias)
					if a != "" {
						aliasSet[a] = struct{}{}
					}
				}
				if len(aliasSet) == 0 {
					m.setStatus("ssh: selection is empty (no valid aliases)", 3500)
					return m, tea.ClearScreen
				}

				switch mode {
				case "export":
					n, err := ExportSSHConfigSelected(m.sshXferEntries, aliasSet, p)
					if err != nil {
						m.setStatus(fmt.Sprintf("ssh export failed: %v", err), 4000)
						return m, tea.ClearScreen
					}

					// Close selector after successful export.
					m.showSSHConfigXfer = false
					m.sshXferMode = ""
					m.sshXferEntries = nil
					m.sshXferFilteredIdx = nil
					m.sshXferSelectedSet = make(map[int]struct{})
					m.sshXferSelectedCursor = 0
					m.sshXferScroll = 0
					m.sshXferQuery.SetValue("")
					m.sshXferQuery.Blur()
					m.sshXferStatus = ""

					// Offer to open editor.
					m.showSSHPostWriteConfirm = true
					m.sshPostWritePath = p
					m.sshPostWriteAction = "export"

					m.setStatus(fmt.Sprintf("ssh export: wrote %d host(s) to %s", n, p), 4500)
					return m, tea.ClearScreen

				case "import":
					// Import: merge/update selected hosts into the target ssh config file.
					// Path can be the primary ~/.ssh/config or any other ssh config file path.
					//
					// Semantics (merge/update by alias):
					// - If the Host alias exists in the file, update its settings (HostName/User/Port/ProxyJump/ForwardAgent/IdentityFile)
					//   using values derived from the parsed entries for that alias.
					// - If the alias does not exist, append a new Host block.
					// - Write atomically with a .bak backup (same behavior as other sshconfig writers).
					//
					// NOTE: This does not import into tmux-ssh-manager YAML; it edits an OpenSSH config file.
					targetPath := strings.TrimSpace(p)
					if targetPath == "" {
						m.setStatus("ssh import: empty target path", 3500)
						return m, tea.ClearScreen
					}

					// Load/parse the target ssh config file if present; else start empty.
					targetPath = expandUserAndEnv(targetPath)
					if ap, err := filepath.Abs(targetPath); err == nil {
						targetPath = ap
					}
					var fileLines []string
					if data, rerr := os.ReadFile(targetPath); rerr == nil {
						txt := strings.ReplaceAll(string(data), "\r\n", "\n")
						txt = strings.ReplaceAll(txt, "\r", "\n")
						parts := strings.Split(txt, "\n")
						if len(parts) > 0 && parts[len(parts)-1] == "" {
							parts = parts[:len(parts)-1]
						}
						fileLines = parts
					} else if os.IsNotExist(rerr) {
						fileLines = []string{}
					} else if rerr != nil {
						m.setStatus(fmt.Sprintf("ssh import: read %s failed: %v", targetPath, rerr), 4000)
						return m, tea.ClearScreen
					}

					// Parse target file into structured blocks (so we can update existing Host blocks).
					// We parse ONLY this file (no Includes) because we are writing back to this file path.
					sf, perr := LoadSSHConfig(targetPath)
					_ = sf
					_ = perr
					// We still need a structured parse for block spans; use parseSSHConfigRecursive on just this file.
					// Best-effort: if parsing fails, we will fall back to appending blocks.
					var structured *SSHConfigFile
					{
						abs := targetPath
						entries, err := parseSSHConfigRecursive(abs, map[string]struct{}{})
						if err == nil {
							// Reconstruct a minimal "structured" view by reloading the file and building blocks
							// using the existing structured loader (which relies on the same file on disk).
							// NOTE: parseSSHConfigRecursive returns host entries only.
							_ = entries
							if sf2, serr := LoadSSHConfigPrimaryStructured(); serr == nil && sf2 != nil {
								// Only use it if it's actually the same file path.
								if strings.TrimSpace(sf2.Path) == strings.TrimSpace(abs) {
									structured = sf2
									fileLines = sf2.Lines
								}
							}
						}
					}

					// Build selected alias list deterministically.
					aliases := make([]string, 0, len(aliasSet))
					for a := range aliasSet {
						a = strings.TrimSpace(a)
						if a != "" {
							aliases = append(aliases, a)
						}
					}
					sort.Strings(aliases)

					updated := 0
					added := 0

					// Helper: build settings map from the parsed entry for an alias.
					buildSettingsForAlias := func(alias string) map[string][]string {
						settings := map[string][]string{}
						// Start with defaults from the selected entries (last-wins for scalars; identityfiles accumulate).
						hostName := ""
						user := ""
						port := 0
						proxyJump := ""
						var forwardAgent *bool
						idFiles := []string(nil)

						addUnique := func(dst []string, v string) []string {
							v = strings.TrimSpace(v)
							if v == "" {
								return dst
							}
							for _, e := range dst {
								if e == v {
									return dst
								}
							}
							return append(dst, v)
						}

						for _, e := range m.sshXferEntries {
							if strings.TrimSpace(e.Alias) != alias {
								continue
							}
							if strings.TrimSpace(e.HostName) != "" {
								hostName = strings.TrimSpace(e.HostName)
							}
							if strings.TrimSpace(e.User) != "" {
								user = strings.TrimSpace(e.User)
							}
							if e.Port > 0 {
								port = e.Port
							}
							if strings.TrimSpace(e.ProxyJump) != "" {
								proxyJump = strings.TrimSpace(e.ProxyJump)
							}
							if e.ForwardAgent != nil {
								b := *e.ForwardAgent
								forwardAgent = &b
							}
							for _, id := range e.IdentityFiles {
								idFiles = addUnique(idFiles, id)
							}
						}

						// Always write HostName (default to alias if empty) for predictability.
						if strings.TrimSpace(hostName) == "" {
							hostName = alias
						}
						settings["hostname"] = []string{hostName}

						if strings.TrimSpace(user) != "" {
							settings["user"] = []string{user}
						}
						if port > 0 {
							settings["port"] = []string{strconv.Itoa(port)}
						}
						if strings.TrimSpace(proxyJump) != "" {
							settings["proxyjump"] = []string{proxyJump}
						}
						if forwardAgent != nil {
							if *forwardAgent {
								settings["forwardagent"] = []string{"yes"}
							} else {
								settings["forwardagent"] = []string{"no"}
							}
						}
						if len(idFiles) > 0 {
							settings["identityfile"] = append([]string(nil), idFiles...)
						}
						return settings
					}

					// Helper: find last literal Host block for alias in the structured file.
					findLastAliasBlock := func(alias string) *SSHConfigBlock {
						if structured == nil {
							return nil
						}
						var last *SSHConfigBlock
						for i := range structured.Blocks {
							b := structured.Blocks[i]
							for _, pat := range b.Patterns {
								if strings.TrimSpace(pat) == alias {
									last = &structured.Blocks[i]
									break
								}
							}
						}
						return last
					}

					// Apply updates/appends.
					for _, alias := range aliases {
						settings := buildSettingsForAlias(alias)
						if len(settings) == 0 {
							continue
						}

						if b := findLastAliasBlock(alias); b != nil && b.StartLine > 0 && b.EndLine >= b.StartLine {
							// Replace the last matching block in-place.
							indent := detectSSHIndent(fileLines)
							repl := renderSSHHostBlockLines([]string{alias}, settings, indent)
							// StartLine/EndLine are 1-based; spliceLines is 0-based inclusive.
							fileLines = spliceLines(fileLines, b.StartLine-1, b.EndLine-1, repl)
							updated++
							// Re-parse best-effort so subsequent edits operate on current spans.
							entries, err := parseSSHConfigRecursive(targetPath, map[string]struct{}{})
							if err == nil {
								// parseSSHConfigRecursive returns entries only; we keep fileLines as-is.
								_ = entries
							}
							continue
						}

						// Not found: append a new block.
						indent := "  "
						if len(fileLines) > 0 {
							indent = detectSSHIndent(fileLines)
							if strings.TrimSpace(fileLines[len(fileLines)-1]) != "" {
								fileLines = append(fileLines, "")
							}
						}
						fileLines = append(fileLines, renderSSHHostBlockLines([]string{alias}, settings, indent)...)
						fileLines = append(fileLines, "")
						added++
						// Re-parse best-effort for subsequent lookups.
						entries, err := parseSSHConfigRecursive(targetPath, map[string]struct{}{})
						if err == nil {
							// parseSSHConfigRecursive returns entries only; we keep fileLines as-is.
							_ = entries
						}
					}

					if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
						m.setStatus(fmt.Sprintf("ssh import: create dir failed: %v", err), 4000)
						return m, tea.ClearScreen
					}
					if err := writeSSHConfigAtomicWithBackup(targetPath, fileLines); err != nil {
						m.setStatus(fmt.Sprintf("ssh import: write failed: %v", err), 4000)
						return m, tea.ClearScreen
					}

					// Close selector after successful import.
					m.showSSHConfigXfer = false
					m.sshXferMode = ""
					m.sshXferEntries = nil
					m.sshXferFilteredIdx = nil
					m.sshXferSelectedSet = make(map[int]struct{})
					m.sshXferSelectedCursor = 0
					m.sshXferScroll = 0
					m.sshXferQuery.SetValue("")
					m.sshXferQuery.Blur()
					m.sshXferStatus = ""

					// Offer to open editor to review the modified ssh config.
					m.showSSHPostWriteConfirm = true
					m.sshPostWritePath = targetPath
					m.sshPostWriteAction = "import"

					m.setStatus(fmt.Sprintf("ssh import: %d added, %d updated -> %s", added, updated, targetPath), 5000)
					return m, tea.ClearScreen

				default:
					m.setStatus("ssh: invalid path mode", 2500)
					return m, tea.ClearScreen
				}

			default:
				m.sshPathInput.Focus()
				var cmd tea.Cmd
				m.sshPathInput, cmd = m.sshPathInput.Update(msg)
				return m, cmd
			}
		}

		// SSH config import/export modal: highest priority within normal UI (after global keys).
		if m.showSSHConfigXfer {
			switch msg.String() {
			case "q":
				// In this modal, q should cancel/close (never quit the whole program).
				m.showSSHConfigXfer = false
				m.sshXferMode = ""
				m.sshXferEntries = nil
				m.sshXferFilteredIdx = nil
				m.sshXferSelectedSet = make(map[int]struct{})
				m.sshXferSelectedCursor = 0
				m.sshXferScroll = 0
				m.sshXferQuery.SetValue("")
				m.sshXferQuery.Blur()
				m.sshXferStatus = ""
				m.setStatus("ssh xfer: cancelled", 1200)
				return m, tea.ClearScreen

			case "esc":
				// Toggle filter focus
				if m.sshXferQuery.Focused() {
					m.sshXferQuery.Blur()
				} else {
					m.sshXferQuery.Focus()
				}
				return m, nil

			case "/":
				m.sshXferQuery.Focus()
				return m, nil

			case "ctrl+a":
				// Quick select all (within the current filtered set if present; else all entries).
				if len(m.sshXferFilteredIdx) > 0 {
					for _, idx := range m.sshXferFilteredIdx {
						m.sshXferSelectedSet[idx] = struct{}{}
					}
					m.sshXferStatus = fmt.Sprintf("selected %d (filtered)", len(m.sshXferSelectedSet))
				} else {
					for i := range m.sshXferEntries {
						m.sshXferSelectedSet[i] = struct{}{}
					}
					m.sshXferStatus = fmt.Sprintf("selected %d", len(m.sshXferSelectedSet))
				}
				return m, nil

			case "ctrl+d":
				// Clear selection
				m.sshXferSelectedSet = make(map[int]struct{})
				m.sshXferStatus = "selection cleared"
				return m, nil

			case "enter", " ":
				// Toggle selection on the current cursor row.
				idx := -1
				if len(m.sshXferFilteredIdx) > 0 {
					if m.sshXferSelectedCursor >= 0 && m.sshXferSelectedCursor < len(m.sshXferFilteredIdx) {
						idx = m.sshXferFilteredIdx[m.sshXferSelectedCursor]
					}
				} else {
					if m.sshXferSelectedCursor >= 0 && m.sshXferSelectedCursor < len(m.sshXferEntries) {
						idx = m.sshXferSelectedCursor
					}
				}
				if idx >= 0 && idx < len(m.sshXferEntries) {
					if _, ok := m.sshXferSelectedSet[idx]; ok {
						delete(m.sshXferSelectedSet, idx)
					} else {
						m.sshXferSelectedSet[idx] = struct{}{}
					}
					m.sshXferStatus = fmt.Sprintf("selected %d", len(m.sshXferSelectedSet))
				}
				return m, nil

			case "j", "down":
				// Move cursor down (bounded)
				maxN := len(m.sshXferEntries)
				if len(m.sshXferFilteredIdx) > 0 {
					maxN = len(m.sshXferFilteredIdx)
				}
				if maxN <= 0 {
					return m, nil
				}
				if m.sshXferSelectedCursor < maxN-1 {
					m.sshXferSelectedCursor++
				}
				m.pendingG = false
				m.numBuf = ""
				return m, nil

			case "k", "up":
				// Move cursor up (bounded)
				maxN := len(m.sshXferEntries)
				if len(m.sshXferFilteredIdx) > 0 {
					maxN = len(m.sshXferFilteredIdx)
				}
				if maxN <= 0 {
					return m, nil
				}
				if m.sshXferSelectedCursor > 0 {
					m.sshXferSelectedCursor--
				}
				m.pendingG = false
				m.numBuf = ""
				return m, nil

			case "g":
				// Vim-ish: gg to top
				if m.pendingG {
					m.sshXferSelectedCursor = 0
					m.pendingG = false
					m.numBuf = ""
					return m, nil
				}
				m.pendingG = true
				m.numBuf = ""
				return m, nil

			case "G":
				// Vim-ish: G to bottom
				maxN := len(m.sshXferEntries)
				if len(m.sshXferFilteredIdx) > 0 {
					maxN = len(m.sshXferFilteredIdx)
				}
				if maxN > 0 {
					m.sshXferSelectedCursor = maxN - 1
				}
				m.pendingG = false
				m.numBuf = ""
				return m, nil

			case "e":
				// Export: prompt for output path (absolute or relative to cwd)
				if strings.ToLower(strings.TrimSpace(m.sshXferMode)) != "export" {
					m.setStatus("ssh xfer: not in export mode", 2000)
					return m, nil
				}
				if len(m.sshXferSelectedSet) == 0 {
					m.setStatus("ssh export: nothing selected (use Space/Enter to select, ctrl+a for all)", 3500)
					return m, nil
				}
				m.showSSHPathPrompt = true
				m.sshPathMode = "export"
				m.sshPathHint = "Enter output path (abs or relative to cwd)"
				// Pre-fill with the previous default for convenience.
				def := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_SSH_EXPORT_PATH"))
				if def == "" {
					if cfgDir, derr := DefaultConfigDir(); derr == nil && strings.TrimSpace(cfgDir) != "" {
						def = filepath.Join(cfgDir, "sshconfig.exported")
					}
				}
				m.sshPathInput.SetValue(def)
				m.sshPathInput.Focus()
				return m, tea.ClearScreen

			case "i":
				// Import: prompt for target ssh config path (abs or relative to cwd).
				// Default: primary OpenSSH config (~/.ssh/config).
				if strings.ToLower(strings.TrimSpace(m.sshXferMode)) != "import" {
					m.setStatus("ssh xfer: not in import mode", 2000)
					return m, nil
				}
				if len(m.sshXferSelectedSet) == 0 {
					m.setStatus("ssh import: nothing selected (use Space/Enter to select, ctrl+a for all)", 3500)
					return m, nil
				}
				m.showSSHPathPrompt = true
				m.sshPathMode = "import"
				m.sshPathHint = "Enter target ssh config path (abs or relative to cwd)"
				def, err := LoadSSHConfigPrimaryPath()
				if err != nil || strings.TrimSpace(def) == "" {
					def = filepath.Join(os.Getenv("HOME"), ".ssh", "config")
				}
				m.sshPathInput.SetValue(def)
				m.sshPathInput.Focus()
				return m, tea.ClearScreen

			default:
				// If filter input is focused, let it consume keystrokes.
				if m.sshXferQuery.Focused() {
					var cmd tea.Cmd
					m.sshXferQuery, cmd = m.sshXferQuery.Update(msg)
					// Note: filtering logic is handled elsewhere; this modal just captures input state.
					return m, cmd
				}
			}
		}

		// Add SSH Host modal (primary ~/.ssh/config): highest priority within normal UI (after global keys).
		if m.showAddSSHHost {
			switch msg.String() {
			case "q":
				// In this modal, q should cancel/close (never quit the whole program).
				m.showAddSSHHost = false
				m.addSSHFieldSel = 0
				m.addSSHCmdMode = false
				m.pendingG = false
				m.numBuf = ""
				m.addSSHAlias.Blur()
				m.addSSHHostName.Blur()
				m.addSSHUser.Blur()
				m.addSSHPort.Blur()
				m.addSSHProxyJump.Blur()
				m.addSSHIdentityFile.Blur()
				m.setStatus("add host: cancelled", 1200)
				return m, tea.ClearScreen

			case "ctrl+e":
				// Open primary SSH config in vi.
				//
				// Popup-aware behavior:
				// - In a tmux popup, splits/windows are hidden behind the popup. Instead, open a nested
				//   tmux popup running vi, then return when vi exits.
				// - Otherwise, open vi in a tmux split for a nicer workflow.
				primary, err := LoadSSHConfigPrimaryPath()
				if err != nil {
					m.setStatus(fmt.Sprintf("edit ssh config: %v", err), 3500)
					return m, nil
				}

				// Hardcode a safe terminal editor.
				editor := "vi"

				// Best-effort: restore terminal state before launching the editor.
				restoreTerminalForExec()

				if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_IN_POPUP")) == "1" {
					// In a tmux popup, nested popups/splits can be hidden behind the active popup.
					// Run vi in-place by exec'ing a process through Bubble Tea, then return to the TUI.
					//
					// Per requested UX: close the Add SSH Host modal and return to the host list after vi exits.
					m.showAddSSHHost = false
					m.addSSHFieldSel = 0
					m.addSSHCmdMode = false
					m.pendingG = false
					m.numBuf = ""
					m.addSSHAlias.Blur()
					m.addSSHHostName.Blur()
					m.addSSHUser.Blur()
					m.addSSHPort.Blur()
					m.addSSHProxyJump.Blur()
					m.addSSHIdentityFile.Blur()

					restoreTerminalForExec()

					cmd := exec.Command(editor, primary)
					cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

					m.setStatus("ssh config: opening vi...", 1500)
					return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
						if err != nil {
							return errMsg{Err: fmt.Errorf("edit ssh config: %w", err)}
						}
						return statusMsg("ssh config: editor closed")
					})
				}

				// Non-popup: run editor inside a new tmux split, wait for it to exit, then return.
				cmdline := fmt.Sprintf("exec %s %q", editor, primary)
				cmd := exec.Command("tmux", "split-window", "-v", "-c", "#{pane_current_path}", "bash", "-lc", cmdline)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				if err := cmd.Run(); err != nil {
					m.setStatus(fmt.Sprintf("edit ssh config: %v", err), 3500)
					return m, nil
				}

				m.setStatus("ssh config: editor closed", 1500)
				// Note: we intentionally keep the Add SSH Host modal open so the user can continue
				// (or cancel) without losing typed values.
				return m, tea.ClearScreen

			case "esc":
				// In Add Host, Esc toggles command mode:
				// - cmd mode OFF (default): insert mode; keys are passed to the focused field
				// - cmd mode ON: vim-ish navigation (0/g/G + numeric jump)
				m.addSSHCmdMode = !m.addSSHCmdMode
				m.pendingG = false
				m.numBuf = ""
				if m.addSSHCmdMode {
					// When entering cmd mode, blur inputs so it's clear you're navigating.
					m.addSSHAlias.Blur()
					m.addSSHHostName.Blur()
					m.addSSHUser.Blur()
					m.addSSHPort.Blur()
					m.addSSHProxyJump.Blur()
					m.addSSHIdentityFile.Blur()
					m.setStatus("add host: COMMAND mode (0/g/G + 1-7 then Enter jump). Esc to return to insert.", 3500)
				} else {
					m.setStatus("add host: INSERT mode (type values). Esc to enter command mode.", 2500)
				}
				return m, nil

			case "0":
				// Only treat 0 as a jump when command mode is enabled.
				if !m.addSSHCmdMode {
					m.pendingG = false
					break // let the focused input receive "0"
				}
				// Vim-ish: 0 goes to top (Alias).
				m.addSSHFieldSel = 0
				m.pendingG = false
				m.numBuf = ""
				return m, nil

			case "g":
				// Only treat g as navigation when command mode is enabled.
				if !m.addSSHCmdMode {
					m.pendingG = false
					break // let the focused input receive "g"
				}
				// Vim-ish: gg to top
				if m.pendingG {
					m.addSSHFieldSel = 0
					m.pendingG = false
					m.numBuf = ""
					return m, nil
				}
				m.pendingG = true
				m.numBuf = ""
				return m, nil

			case "G":
				// Only treat G as navigation when command mode is enabled.
				if !m.addSSHCmdMode {
					m.pendingG = false
					break // let the focused input receive "G"
				}
				// Vim-ish: G to bottom (IdentityFile)
				m.addSSHFieldSel = 6
				m.pendingG = false
				m.numBuf = ""
				return m, nil

			case "1", "2", "3", "4", "5", "6", "7":
				// Numeric jump only in command mode.
				// In insert mode, digits should be inserted into the focused field (e.g. Port).
				if !m.addSSHCmdMode {
					m.pendingG = false
					break // let the focused input receive the digit
				}
				// Command mode: treat as numeric jump buffer (rows shown are 1-based in UI here):
				// 1 Alias, 2 HostName, 3 User, 4 Port, 5 ProxyJump, 6 ForwardAgent, 7 IdentityFile.
				m.pendingG = false
				m.numBuf = m.numBuf + msg.String()
				return m, nil

			case "tab", "enter":
				// If a numeric field selection was typed (e.g. "6" then Enter), jump to that row.
				//
				// IMPORTANT:
				// Only honor numeric-jump when command mode is enabled. In insert mode, digits are input.
				if m.addSSHCmdMode && strings.TrimSpace(m.numBuf) != "" {
					if n, err := strconv.Atoi(strings.TrimSpace(m.numBuf)); err == nil {
						if n >= 1 && n <= 7 {
							m.addSSHFieldSel = n - 1
						} else {
							m.setStatus("add host: invalid field number (use 1-7)", 2500)
						}
					}
					m.numBuf = ""
					m.pendingG = false
					return m, nil
				}

				// Enter on last field triggers save; otherwise advances.
				if m.addSSHFieldSel < 6 {
					m.addSSHFieldSel++
				} else {
					// Save
					alias := strings.TrimSpace(m.addSSHAlias.Value())
					hostName := strings.TrimSpace(m.addSSHHostName.Value())
					user := strings.TrimSpace(m.addSSHUser.Value())
					portStr := strings.TrimSpace(m.addSSHPort.Value())
					proxyJump := strings.TrimSpace(m.addSSHProxyJump.Value())
					identityFile := strings.TrimSpace(m.addSSHIdentityFile.Value())

					port := 0
					if portStr != "" {
						if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
							port = p
						} else {
							m.setStatus("add host: invalid port (must be a positive integer)", 3500)
							return m, nil
						}
					}

					if err := AppendPrimaryHostBlock(AddPrimaryHostParams{
						Alias:        alias,
						HostName:     hostName,
						User:         user,
						Port:         port,
						ProxyJump:    proxyJump,
						ForwardAgent: m.addSSHForwardAgent, // default true
						IdentityFile: identityFile,
					}); err != nil {
						m.setStatus(fmt.Sprintf("add host failed: %v", err), 4500)
						return m, nil
					}

					// Close modal + refresh
					m.showAddSSHHost = false
					m.addSSHFieldSel = 0
					m.addSSHCmdMode = false
					m.pendingG = false
					m.numBuf = ""
					m.addSSHAlias.SetValue("")
					m.addSSHHostName.SetValue("")
					m.addSSHUser.SetValue("")
					m.addSSHPort.SetValue("")
					m.addSSHProxyJump.SetValue("")
					m.addSSHIdentityFile.SetValue("")
					m.addSSHForwardAgent = true

					// Refresh duplicates map and TUI candidates (best-effort).
					if rep, err := ComputePrimaryDuplicateAliases(); err == nil && rep != nil && rep.AliasToBlocks != nil {
						m.dupAliasCount = make(map[string]int)
						for a, blocks := range rep.AliasToBlocks {
							a = strings.TrimSpace(a)
							if a == "" {
								continue
							}
							m.dupAliasCount[a] = len(blocks)
						}
					}
					if conf, err := LoadConfigFromSSH(); err == nil && conf != nil {
						m.cfg = conf
						m.candidates = buildCandidates(conf)
						m.recomputeFilter()
					}

					m.setStatus(fmt.Sprintf("added host: %s", alias), 2500)
					return m, tea.ClearScreen
				}

			case "shift+tab":
				m.pendingG = false
				m.numBuf = ""
				if m.addSSHFieldSel > 0 {
					m.addSSHFieldSel--
				}
				return m, nil

			case "j", "down":
				// Uniform vim-ish navigation for this modal: j/k move between fields.
				m.pendingG = false
				m.numBuf = ""
				if m.addSSHFieldSel < 6 {
					m.addSSHFieldSel++
				}
				return m, nil

			case "k", "up":
				m.pendingG = false
				m.numBuf = ""
				if m.addSSHFieldSel > 0 {
					m.addSSHFieldSel--
				}
				return m, nil

			case " ":
				// Space toggles ForwardAgent when focused on that row.
				m.pendingG = false
				m.numBuf = ""
				if m.addSSHFieldSel == 5 {
					m.addSSHForwardAgent = !m.addSSHForwardAgent
					return m, nil
				}
			default:
				// Any other key cancels a pending "g" sequence.
				m.pendingG = false
			}

			// Focus management + let focused input update.
			m.addSSHAlias.Blur()
			m.addSSHHostName.Blur()
			m.addSSHUser.Blur()
			m.addSSHPort.Blur()
			m.addSSHProxyJump.Blur()
			m.addSSHIdentityFile.Blur()

			switch m.addSSHFieldSel {
			case 0:
				m.addSSHAlias.Focus()
				var cmd tea.Cmd
				m.addSSHAlias, cmd = m.addSSHAlias.Update(msg)
				return m, cmd
			case 1:
				m.addSSHHostName.Focus()
				var cmd tea.Cmd
				m.addSSHHostName, cmd = m.addSSHHostName.Update(msg)
				return m, cmd
			case 2:
				m.addSSHUser.Focus()
				var cmd tea.Cmd
				m.addSSHUser, cmd = m.addSSHUser.Update(msg)
				return m, cmd
			case 3:
				m.addSSHPort.Focus()
				var cmd tea.Cmd
				m.addSSHPort, cmd = m.addSSHPort.Update(msg)
				return m, cmd
			case 4:
				m.addSSHProxyJump.Focus()
				var cmd tea.Cmd
				m.addSSHProxyJump, cmd = m.addSSHProxyJump.Update(msg)
				return m, cmd
			case 6:
				m.addSSHIdentityFile.Focus()
				var cmd tea.Cmd
				m.addSSHIdentityFile, cmd = m.addSSHIdentityFile.Update(msg)
				return m, cmd
			default:
				// ForwardAgent row (5) isn't a textinput; ignore.
				return m, nil
			}
		}

		// Device OS editor modal (stored in HostExtras.device_os)
		if m.showDeviceOSEditor {
			switch msg.String() {
			case "esc", "q":
				m.showDeviceOSEditor = false
				m.deviceOSInput.Blur()
				m.pendingG = false
				m.numBuf = ""
				m.setStatus("device os: cancelled", 1200)
				return m, tea.ClearScreen

			case "enter":
				sel := m.current()
				if sel == nil {
					m.setStatus("device os: no host selected", 1500)
					return m, nil
				}
				hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
				if hostKey == "" {
					m.setStatus("device os: empty host key", 1500)
					return m, nil
				}

				val := strings.ToLower(strings.TrimSpace(m.deviceOSInput.Value()))
				// Allow empty to clear.
				if val != "" {
					switch val {
					case "cisco_iosxe", "sonic_dell":
						// ok
					default:
						m.setStatus("device os: invalid (expected: cisco_iosxe|sonic_dell)", 4500)
						return m, nil
					}
				}

				ex, _ := LoadHostExtras(hostKey)
				ex.HostKey = hostKey
				ex.DeviceOS = val
				if err := SaveHostExtras(ex); err != nil {
					m.setStatus(fmt.Sprintf("device os save failed: %v", err), 3500)
					return m, nil
				}

				m.showDeviceOSEditor = false
				m.deviceOSInput.Blur()
				if val == "" {
					m.setStatus("device os: cleared", 2000)
				} else {
					m.setStatus(fmt.Sprintf("device os: %s", val), 2500)
				}
				return m, tea.ClearScreen

			default:
				m.deviceOSInput.Focus()
				var cmd tea.Cmd
				m.deviceOSInput, cmd = m.deviceOSInput.Update(msg)
				return m, cmd
			}
		}

		// Host Settings overlay: modal navigation + actions
		if m.showHostSettings {
			switch msg.String() {
			case "esc", "q":
				m.showHostSettings = false
				m.pendingG = false
				m.numBuf = ""
				m.input.Blur()
				m.recomputeFilter()
				return m, tea.ClearScreen

			case "0":
				// Buffer digits for numeric selection (supports multi-digit like "10" + Enter).
				// If you want vim-ish "go to top", use "gg".
				m.pendingG = false
				m.numBuf = m.numBuf + msg.String()
				return m, nil

			case "g":
				// Vim-ish: gg to top
				if m.pendingG {
					m.hostSettingsSel = 0
					m.pendingG = false
					m.numBuf = ""
					return m, nil
				}
				m.pendingG = true
				m.numBuf = ""
				return m, nil

			case "G":
				// Vim-ish: G to bottom
				m.hostSettingsSel = 9
				m.pendingG = false
				m.numBuf = ""
				return m, nil

			case "j", "down":
				m.pendingG = false
				m.numBuf = ""
				if m.hostSettingsSel < 9 {
					m.hostSettingsSel++
				}
				return m, nil

			case "k", "up":
				m.pendingG = false
				m.numBuf = ""
				if m.hostSettingsSel > 0 {
					m.hostSettingsSel--
				}
				return m, nil

			case "1", "2", "3", "4", "5", "6", "7", "8", "9":
				// Numeric selection (menu items are 1-based in UI).
				// Support multi-digit selection by buffering digits and applying on Enter.
				m.pendingG = false
				m.numBuf = m.numBuf + msg.String()
				return m, nil

			case "enter":
				// If a numeric menu selection was typed (e.g. "10" then Enter), apply it.
				if strings.TrimSpace(m.numBuf) != "" {
					if n, err := strconv.Atoi(strings.TrimSpace(m.numBuf)); err == nil {
						maxItem := 10
						if n >= 1 && n <= maxItem {
							m.hostSettingsSel = n - 1
						} else {
							m.setStatus("host settings: invalid selection", 2000)
							m.numBuf = ""
							return m, nil
						}
					}
					m.numBuf = ""
				}

				// 0: Login mode toggle
				// 1: Credential set
				// 2: Credential delete
				// 3: Logging toggle
				// 4: Install my public key (authorized_keys)
				// 5: Add SSH Host (append to ~/.ssh/config)
				// 6: Set Router-ID (HostExtras.router_id)
				// 7: Set Mgmt IP (HostExtras.mgmt_ip)
				// 8: Set Neighbor Discovery (HostExtras.neighbor_discovery)
				// 9: Set Device OS (HostExtras.device_os)
				//
				// Note:
				// Add SSH Host should be available even when no host is selected.
				if m.hostSettingsSel == 5 {
					// IMPORTANT:
					// Close Host Settings when opening the Add SSH Host modal; otherwise the Host Settings
					// overlay will continue to render and the new modal will appear "frozen"/hidden.
					m.showHostSettings = false
					m.pendingG = false

					// Add SSH Host (append to ~/.ssh/config). Defaults ForwardAgent=yes.
					m.showAddSSHHost = true
					m.addSSHFieldSel = 0
					m.addSSHAlias.SetValue("")
					m.addSSHHostName.SetValue("")
					m.addSSHUser.SetValue("")
					m.addSSHPort.SetValue("")
					m.addSSHProxyJump.SetValue("")
					m.addSSHIdentityFile.SetValue("")
					m.addSSHForwardAgent = true
					m.addSSHAlias.Focus()
					m.setStatus("add host: fill fields, Enter to advance, Enter on last field to save", 3500)
					return m, tea.ClearScreen
				}

				if sel := m.current(); sel != nil {
					hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
					if hostKey == "" {
						m.setStatus("host settings: no host selected", 1500)
						return m, nil
					}

					// Debug/status so it’s obvious Enter was handled and which action is being executed.

					switch m.hostSettingsSel {
					case 9:
						// Device OS editor (HostExtras.device_os). This controls topology discovery command selection.
						ex, _ := LoadHostExtras(hostKey)
						m.showDeviceOSEditor = true
						m.deviceOSInput.SetValue(strings.TrimSpace(ex.DeviceOS))
						m.deviceOSInput.Focus()
						m.setStatus("device os: enter cisco_iosxe|sonic_dell, Enter to save, empty to clear", 6000)
						// Close Host Settings while the editor modal is open (modal priority).
						m.showHostSettings = false
						m.pendingG = false
						m.numBuf = ""
						return m, tea.ClearScreen

					case 8:
						// Neighbor discovery preference for topology (auto/lldp/cdp).
						// Stored in per-host extras as: neighbor_discovery=auto|lldp|cdp
						//
						// Semantics (explicit):
						// - auto: use OS-specific fallback chain (e.g. IOS/XE: LLDP detail -> LLDP summary -> CDP)
						// - lldp: LLDP ONLY (no CDP fallback)
						// - cdp : CDP ONLY (no LLDP fallback; only meaningful on Cisco IOS/XE)
						//
						// NOTE:
						// The Network View builds a generic topology from parsed neighbor edges; it does not care
						// whether edges came from LLDP or CDP. This setting only controls which discovery commands
						// are attempted for this host during background collection.
						ex, _ := LoadHostExtras(hostKey)
						ex.HostKey = hostKey
						cur := strings.ToLower(strings.TrimSpace(ex.NeighborDiscovery))
						next := "auto"
						switch cur {
						case "", "auto":
							next = "lldp"
						case "lldp":
							next = "cdp"
						case "cdp":
							next = "auto"
						default:
							next = "auto"
						}
						ex.NeighborDiscovery = next
						if err := SaveHostExtras(ex); err != nil {
							m.setStatus(fmt.Sprintf("neighbor discovery save failed: %v", err), 3500)
							return m, nil
						}
						m.setStatus(fmt.Sprintf("neighbor discovery: %s", next), 2500)
						return m, nil

					case 6:
						// Router-ID editor (HostExtras.router_id). This is used for topology matching only.
						ex, _ := LoadHostExtras(hostKey)
						m.showRouterIDEditor = true
						m.routerIDInput.SetValue(strings.TrimSpace(ex.RouterID))
						m.routerIDInput.Focus()
						m.setStatus("router-id: enter IPv4/IPv6 (loopback), Enter to save, empty to clear", 5000)
						// Close Host Settings while the editor modal is open (modal priority).
						m.showHostSettings = false
						m.pendingG = false
						m.numBuf = ""
						return m, tea.ClearScreen

					case 7:
						// Mgmt IP editor (HostExtras.mgmt_ip). This is used for topology matching only.
						ex, _ := LoadHostExtras(hostKey)
						m.showMgmtIPEditor = true
						m.mgmtIPInput.SetValue(strings.TrimSpace(ex.MgmtIP))
						m.mgmtIPInput.Focus()
						m.setStatus("mgmt ip: enter IPv4/IPv6, Enter to save, empty to clear", 5000)
						// Close Host Settings while the editor modal is open (modal priority).
						m.showHostSettings = false
						m.pendingG = false
						m.numBuf = ""
						return m, tea.ClearScreen

					case 0:
						// Toggle effective login mode via HostExtras auth_mode
						ex, _ := LoadHostExtras(hostKey)
						ex.HostKey = hostKey
						am := strings.ToLower(strings.TrimSpace(ex.AuthMode))
						if am == "" || am == "manual" {
							ex.AuthMode = "keychain" // maps to askpass
						} else {
							ex.AuthMode = "manual"
						}
						if err := SaveHostExtras(ex); err != nil {
							m.setStatus(fmt.Sprintf("login mode save failed: %v", err), 3500)
							return m, nil
						}

						// Usability: if askpass was enabled, immediately warn if the Keychain credential is missing.
						effective := m.effectiveLoginMode(sel.Resolved)
						if strings.EqualFold(strings.TrimSpace(effective), "askpass") {
							// Non-revealing existence check
							if err := CredGet(hostKey, sel.Resolved.EffectiveUser, "password"); err != nil {
								m.setStatus(fmt.Sprintf("login mode: askpass enabled, but no credential found — select 'Set credential (system store)' and press Enter (%s)", CredentialBackendLabel()), 5000)
								return m, nil
							}
						}

						m.setStatus(fmt.Sprintf("login mode: %s", effective), 2000)
						return m, nil

					case 1:
						// Cred set:
						// - In popup mode: do NOT attempt nested popups. Instead, write a popup action request and quit cleanly.
						//   The popup wrapper will run the interactive `cred set` in the SAME popup pane and then relaunch the TUI.
						// - Outside popup mode: use a foreground tmux popup (best UX) and fall back to a window only if popup fails.
						userArg := strings.TrimSpace(sel.Resolved.EffectiveUser)
						bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
						if bin == "" {
							bin = "tmux-ssh-manager"
						}

						backendLabel := CredentialBackendLabel()

						// Popup mode: request action + quit (wrapper loop will handle it and relaunch TUI).
						if os.Getenv("TMUX_SSH_MANAGER_IN_POPUP") != "" {
							// Auto-enable askpass once the credential is stored.
							ex, _ := LoadHostExtras(hostKey)
							ex.HostKey = hostKey
							ex.AuthMode = "keychain"
							_ = SaveHostExtras(ex)

							// Write action request file under the popup wrapper's config directory.
							// Wrapper reads this file and runs the action in the same popup TTY.
							actionFile := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_POPUP_ACTION_FILE"))
							if actionFile == "" {
								home, _ := os.UserHomeDir()
								if strings.TrimSpace(home) == "" {
									home = "/tmp"
								}
								actionFile = filepath.Join(home, ".config", "tmux-ssh-manager", "popup_action.env")
							}
							_ = os.MkdirAll(filepath.Dir(actionFile), 0o700)
							content := "action=cred-set\n" +
								"host=" + hostKey + "\n" +
								"user=" + userArg + "\n" +
								"kind=password\n"
							_ = os.WriteFile(actionFile, []byte(content), 0o600)

							m.setStatus(fmt.Sprintf("cred: launching prompt (%s)...", backendLabel), 1500)
							m.quitting = true
							return m, tea.Quit
						}

						userFlag := ""
						if userArg != "" {
							userFlag = "--user " + shellEscapeForSh(userArg)
						}

						// Prefer popup. -E closes automatically on success.
						popupCmd := fmt.Sprintf(
							"printf '\033[?25h\033[0m' >/dev/tty 2>/dev/null || true; stty sane </dev/tty >/dev/tty 2>/dev/null || true; %s cred set --host %s %s; rc=$?; echo; if [ $rc -eq 0 ]; then echo 'Saved to system credential store (%s).'; exit 0; else echo 'FAILED (exit='$rc')'; echo; echo 'Press Enter to close...'; read -r _; exit $rc; fi",
							shellEscapeForSh(bin),
							shellEscapeForSh(hostKey),
							userFlag,
							shellEscapeForSh(backendLabel),
						)

						popupErr := exec.Command(
							"tmux", "display-popup",
							"-E",
							"-T", fmt.Sprintf("tmux-ssh-manager: credential for %s", hostKey),
							"-w", "80%",
							"-h", "60%",
							"--",
							"bash", "-lc", popupCmd,
						).Run()

						if popupErr != nil {
							// Fallback: open a window for credential prompting; keep it open until the user confirms.
							_ = exec.Command(
								"tmux", "new-window",
								"-n", "cred-set",
								"bash", "-lc",
								fmt.Sprintf(
									"printf '\033[?25h\033[0m' >/dev/tty 2>/dev/null || true; stty sane </dev/tty >/dev/tty 2>/dev/null || true; %s cred set --host %s %s; rc=$?; echo; if [ $rc -eq 0 ]; then echo 'Saved to system credential store (%s).'; else echo 'FAILED (exit='$rc')'; fi; echo; echo 'Press Enter to close...'; read -r _",
									shellEscapeForSh(bin),
									shellEscapeForSh(hostKey),
									userFlag,
									shellEscapeForSh(backendLabel),
								),
							).Run()
						}

						// Refresh status (non-revealing) after returning.
						if err := CredGet(hostKey, sel.Resolved.EffectiveUser, "password"); err != nil {
							m.setStatus("cred: not set (or unavailable)", 3000)
						} else {
							// UX tweak: if a credential was just set and login mode is still manual,
							// automatically enable askpass by persisting auth_mode=keychain in HostExtras.
							if strings.EqualFold(strings.TrimSpace(m.effectiveLoginMode(sel.Resolved)), "manual") {
								ex, _ := LoadHostExtras(hostKey)
								ex.HostKey = hostKey
								ex.AuthMode = "keychain"
								_ = SaveHostExtras(ex)
								m.setStatus(fmt.Sprintf("cred: stored in system credential store (%s) • login mode: askpass enabled", backendLabel), 2500)
							} else {
								m.setStatus(fmt.Sprintf("cred: stored in system credential store (%s)", backendLabel), 2000)
							}
						}
						return m, nil

					case 2:
						// Cred delete:
						// - In popup mode: write a popup action request and quit cleanly.
						//   The popup wrapper will run the interactive `cred delete` in the SAME popup pane and then relaunch the TUI.
						// - Outside popup mode: delete inline and set login mode back to manual.
						backendLabel := CredentialBackendLabel()

						if os.Getenv("TMUX_SSH_MANAGER_IN_POPUP") != "" {
							userArg := strings.TrimSpace(sel.Resolved.EffectiveUser)

							// Disable askpass now; once the credential is deleted there is nothing to automate.
							ex, _ := LoadHostExtras(hostKey)
							ex.HostKey = hostKey
							ex.AuthMode = "manual"
							_ = SaveHostExtras(ex)

							actionFile := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_POPUP_ACTION_FILE"))
							if actionFile == "" {
								home, _ := os.UserHomeDir()
								if strings.TrimSpace(home) == "" {
									home = "/tmp"
								}
								actionFile = filepath.Join(home, ".config", "tmux-ssh-manager", "popup_action.env")
							}
							_ = os.MkdirAll(filepath.Dir(actionFile), 0o700)
							content := "action=cred-delete\n" +
								"host=" + hostKey + "\n" +
								"user=" + userArg + "\n" +
								"kind=password\n"
							_ = os.WriteFile(actionFile, []byte(content), 0o600)

							m.setStatus(fmt.Sprintf("cred: launching delete (%s)...", backendLabel), 1500)
							m.quitting = true
							return m, tea.Quit
						}

						// Non-popup mode: delete inline (non-interactive).
						if err := CredDelete(hostKey, sel.Resolved.EffectiveUser, "password"); err != nil {
							m.setStatus(fmt.Sprintf("cred delete failed (%s): %v", backendLabel, err), 3500)
							return m, nil
						}

						// Also disable askpass mode since there's no credential to use anymore.
						ex, _ := LoadHostExtras(hostKey)
						ex.HostKey = hostKey
						ex.AuthMode = "manual"
						_ = SaveHostExtras(ex)

						m.setStatus(fmt.Sprintf("cred: deleted from system credential store (%s) • login mode: manual", backendLabel), 2500)
						return m, nil

					case 3:
						// Logging toggle (same semantics as T key)
						ex, _ := LoadHostExtras(hostKey)
						ex.HostKey = hostKey
						ex.Logging = !ex.Logging
						if err := SaveHostExtras(ex); err != nil {
							m.setStatus(fmt.Sprintf("logging toggle failed: %v", err), 3500)
							return m, nil
						}
						if ex.Logging {
							m.setStatus("logging: enabled", 2000)
						} else {
							m.setStatus("logging: disabled", 2000)
						}
						return m, nil

					case 4:
						// Install local SSH public key into remote authorized_keys (new window preferred).
						// This is idempotent by default (ensure mode).
						//
						// Debug/status:
						// - show that we are about to launch and which mode/host/user is being used

						if err := m.tmuxInstallMyKey(sel.Resolved, false); err != nil {

							m.setStatus(fmt.Sprintf("key install: %v", err), 5000)
							return m, nil
						}

						// Keep the modal open so the user can re-run or inspect settings; also give a clear next-step hint.
						m.setStatus("Key install started in a new tmux window (look for output there).", 4500)
						return m, nil

					case 5:
						// Add SSH Host is handled above (allowed even with no current host selection).
						return m, nil
					}
				}
				return m, nil
			default:
				return m, nil
			}
		}

		// Dashboards browser: modal navigation and materialize
		if m.showDashBrowser {
			switch msg.String() {
			case "esc", "q":
				m.showDashBrowser = false
				m.pendingG = false
				m.input.Blur()
				m.recomputeFilter()
				return m, tea.ClearScreen
			case "j", "down":
				// Move within merged dashboards list (YAML + recorded).
				n := 0
				if m.cfg != nil {
					n += len(m.cfg.Dashboards)
				}
				if m.state != nil {
					n += len(m.state.RecordedDashboards)
				}
				if m.dashSelected+1 < n {
					m.dashSelected++
				}
				return m, nil
			case "k", "up":
				if m.dashSelected > 0 {
					m.dashSelected--
				}
				return m, nil
			case "l":
				// Cycle layout override: default -> tiled -> even-h -> even-v -> main-v -> main-h -> default
				m.dashLayoutMode = (m.dashLayoutMode + 1) % 6
				return m, nil
			case "enter":
				// Materialize selected dashboard (YAML + recorded dashboards from state).
				// Must work even if config isn't loaded (recorded dashboards can still be shown).
				allDash := make([]Dashboard, 0, 8)
				if m.cfg != nil && len(m.cfg.Dashboards) > 0 {
					allDash = append(allDash, m.cfg.Dashboards...)
				}
				if m.state != nil && len(m.state.RecordedDashboards) > 0 {
					for _, rdd := range m.state.RecordedDashboards {
						allDash = append(allDash, rdd.ToConfigDashboard())
					}
				}

				if m.dashSelected < 0 || m.dashSelected >= len(allDash) {
					m.setStatus("No dashboard selected", 1500)
					m.showDashBrowser = false
					m.pendingG = false
					m.input.Blur()
					m.recomputeFilter()
					return m, tea.ClearScreen
				}

				if m.cfg == nil {
					m.setStatus("Dashboards require a loaded config (hosts/groups) to resolve panes", 3500)
					m.showDashBrowser = false
					m.pendingG = false
					m.input.Blur()
					m.recomputeFilter()
					return m, tea.ClearScreen
				}

				// Optional enhanced path: export dashboard as a tmux-session-manager spec (YAML/JSON) and apply it
				// via tmux-session-manager in a new tmux window.
				//
				// This is intentionally optional (no hard dependency): we write a spec file under
				// ~/.config/tmux-ssh-manager/dashboards and delegate materialization to tmux-session-manager
				// when enabled.
				//
				// Enable by setting:
				//   - TMUX_SSH_MANAGER_USE_SESSION_MANAGER=1
				//
				// Optional knobs:
				//   - TMUX_SSH_MANAGER_DASH_SPEC_FORMAT=yaml|json              (default: yaml)
				//   - TMUX_SSH_MANAGER_DASH_SPEC_DIR=...                       (default: ~/.config/tmux-ssh-manager/dashboards)
				//   - TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS=1             (default: on)
				//
				// If anything fails, fall back to the built-in dashboard materializer below.
				useSessionMgr := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_USE_SESSION_MANAGER")) != ""
				if useSessionMgr {
					d := allDash[m.dashSelected]
					rd, rerr := m.cfg.ResolveDashboard(d)
					if rerr == nil {
						cfgDir, derr := DefaultConfigDir()
						if derr == nil {
							outDir := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_SPEC_DIR"))
							if outDir == "" {
								outDir = filepath.Join(cfgDir, "dashboards")
							}

							format := strings.ToLower(strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_SPEC_FORMAT")))
							if format != "json" {
								format = "yaml"
							}

							deterministic := true
							if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS")) == "0" {
								deterministic = false
							}

							apply := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_APPLY"))
							if apply == "" {
								apply = "1"
							}

							// Build a manager-agnostic export payload from resolved dashboard data.
							exp := sessionfmt.DashboardExport{
								Name:        strings.TrimSpace(d.Name),
								Description: strings.TrimSpace(d.Description),
								Layout:      strings.TrimSpace(d.Layout),
								Panes:       make([]sessionfmt.DashboardPane, 0, len(rd.Panes)),
							}
							for _, rp := range rd.Panes {
								hostKey := strings.TrimSpace(rp.Target.Host.Name)
								cmds := make([]string, 0, len(rp.EffectiveCommands))
								for _, c := range rp.EffectiveCommands {
									c = strings.TrimSpace(c)
									if c != "" {
										cmds = append(cmds, c)
									}
								}
								exp.Panes = append(exp.Panes, sessionfmt.DashboardPane{
									Title:    strings.TrimSpace(rp.Pane.Title),
									Host:     hostKey,
									Commands: cmds,
									Env:      nil,
								})
							}

							_ = os.MkdirAll(outDir, 0o700)
							outPath, pathErr := sessionfmt.SuggestedDashboardSpecPath(outDir, d.Name, sessionfmt.OutputFormat(format))
							if pathErr != nil {
								// Best-effort: fall back to a simple filename in outDir.
								outPath = filepath.Join(outDir, "dashboard.tmux-session."+format)
							}

							wopt := sessionfmt.WriterOptions{
								SessionName:    "", // let exporter derive
								ExportName:     exp.Name,
								Description:    exp.Description,
								Root:           outDir, // treat export dir as a "project root" for apply
								PreferPanePlan: deterministic,
								Layout:         exp.Layout,
								WindowName:     "dashboard",
								PaneTitles:     true,
								ShellProgram:   "bash",
								ShellFlag:      "-lc",
								Now:            time.Now,
								Attach:         boolPtr(true),
								SwitchClient:   boolPtr(true),
								BaseIndex:      nil,
								PaneBaseIndex:  nil,
							}

							// Write the exported spec file.
							if werr := sessionfmt.WriteDashboardSpec(outPath, sessionfmt.OutputFormat(format), exp, wopt); werr == nil {
								// Apply via tmux-session-manager if enabled.
								if apply != "0" {
									bin := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_BIN"))
									if bin == "" {
										bin = "tmux-session-manager"
									}

									// tmux-session-manager expects project-local spec detection, so we:
									// 1) ensure a ".tmux-session.yaml/.json" exists in outDir
									// 2) run tmux-session-manager with roots=outDir, query selecting this exported dashboard
									// This uses session-manager’s engine (pane_plan, policy enforcement, etc.).
									projectSpecPath := filepath.Join(outDir, ".tmux-session."+format)
									_ = os.WriteFile(projectSpecPath, []byte{}, 0o600) // placeholder to ensure path exists
									_ = os.Rename(outPath, projectSpecPath)

									// Launch session-manager to apply the spec in a new tmux window (consistent UX).
									// Note: allow-shell/passthrough are not enabled here; exported specs are safe by default.
									_ = exec.Command(
										"tmux",
										"new-window",
										"-n", "dash-apply",
										"-c", "#{pane_current_path}",
										"--",
										"bash",
										"-lc",
										fmt.Sprintf("%s --roots %q --prefer-project-spec --query %q", shellEscapeForSh(bin), outDir, filepath.Base(outDir)),
									).Run()

									m.setStatus(fmt.Sprintf("dashboard applied via tmux-session-manager: %s", exp.Name), 2500)
									m.showDashBrowser = false
									m.pendingG = false
									m.input.Blur()
									m.recomputeFilter()
									return m, tea.ClearScreen
								}

								_ = exec.Command("tmux", "display-message", "-d", "2000", fmt.Sprintf("dashboard exported: %s", outPath)).Run()
							}
						}
					}
				}

				d := allDash[m.dashSelected]
				rd, err := m.cfg.ResolveDashboard(d)
				if err != nil {
					m.setStatus(fmt.Sprintf("dashboard error: %v", err), 3000)
					return m, nil
				}
				// Create window or use current
				//
				// IMPORTANT:
				// Dashboards are currently materialized within a single tmux window (splits only).
				// For recording/replay/export to be meaningful and non-destructive, we default to
				// creating a NEW window unless the dashboard explicitly opted out.
				//
				// This ensures:
				// - "save layout" is meaningful (you can always return to your original window)
				// - "windows" semantics in exported specs remain coherent even though we only
				//   support h/v splits here (no multi-window dashboard plan yet).
				var windowID string
				createNewWindow := d.NewWindow
				if !createNewWindow {
					// Default-on safety: avoid mutating the user's current working window when applying dashboards.
					createNewWindow = true
				}

				if createNewWindow {
					out, e := exec.Command("tmux", "new-window", "-P", "-F", "#{window_id}", "-c", "#{pane_current_path}").Output()
					if e != nil {
						m.setStatus(fmt.Sprintf("new-window error: %v", e), 3000)
						return m, nil
					}
					windowID = strings.TrimSpace(string(out))
					if windowID != "" {
						m.createdWindowIDs = append(m.createdWindowIDs, windowID)
					}
				} else {
					// Use current window id
					out, e := exec.Command("tmux", "display-message", "-p", "#{window_id}").Output()
					if e == nil {
						windowID = strings.TrimSpace(string(out))
					}
				}

				// Ensure at least one pane exists; get initial pane_id
				var paneIDs []string
				if windowID != "" {
					out, e := exec.Command("tmux", "list-panes", "-t", windowID, "-F", "#{pane_id}").Output()
					if e == nil {
						lines := strings.Split(strings.TrimSpace(string(out)), "\n")
						for _, ln := range lines {
							p := strings.TrimSpace(ln)
							if p != "" {
								paneIDs = append(paneIDs, p)
							}
						}
					}
				}

				// Create required panes
				for i := 1; i < len(rd.Panes); i++ {
					// Alternate splits for simplicity
					splitArg := "-h"
					if i%2 == 1 {
						splitArg = "-v"
					}
					out, e := exec.Command("tmux", "split-window", splitArg, "-P", "-F", "#{pane_id}", "-c", "#{pane_current_path}").Output()
					if e != nil {
						m.setStatus(fmt.Sprintf("split error: %v", e), 3000)
						return m, nil
					}
					pid := strings.TrimSpace(string(out))
					if pid != "" {
						m.createdPaneIDs = append(m.createdPaneIDs, pid)
						paneIDs = append(paneIDs, pid)
					}
				}

				// Apply layout (either override or dashboard-provided layout).
				// Recorded dashboards may include a captured tmux layout string (#{window_layout}),
				// stored in d.Layout via RecordedDashboard.ToConfigDashboard().
				if windowID != "" {
					override := ""
					switch m.dashLayoutMode {
					case 1:
						override = "tiled"
					case 2:
						override = "even-horizontal"
					case 3:
						override = "even-vertical"
					case 4:
						override = "main-vertical"
					case 5:
						override = "main-horizontal"
					}
					if override != "" {
						_ = exec.Command("tmux", "select-layout", "-t", windowID, override).Run()
					} else if strings.TrimSpace(d.Layout) != "" {
						_ = exec.Command("tmux", "select-layout", "-t", windowID, strings.TrimSpace(d.Layout)).Run()
					}
				}

				// Populate panes: run SSH and send commands
				for i, rp := range rd.Panes {
					var targetPane string
					if i < len(paneIDs) {
						targetPane = paneIDs[i]
					} else {
						// fallback: current pane
						out, e := exec.Command("tmux", "display-message", "-p", "#{pane_id}").Output()
						if e == nil {
							targetPane = strings.TrimSpace(string(out))
						}
					}
					// Enable per-host daily logging for this pane (before starting SSH),
					// so all SSH output is captured without breaking the TTY.
					_ = enableTmuxPipePaneLoggingForTarget(targetPane, rp.Target.Host.Name)

					// In target pane, run SSH
					//
					// IMPORTANT:
					// - Users may alias `ssh` -> `tmux-ssh-manager ssh` (or other functions), which can cause recursion if we
					//   execute a plain `ssh ...` shell line.
					// - We also want tmux-ssh-manager's askpass/keychain behavior when login_mode=askpass.
					//
					// Therefore, prefer invoking tmux-ssh-manager's public ssh wrapper explicitly.
					// It will decide whether to use __connect (PTY + Keychain) or fall back to system ssh.
					bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
					if bin == "" {
						bin = "tmux-ssh-manager"
					}
					hostKey := strings.TrimSpace(rp.Target.Host.Name)
					userArg := strings.TrimSpace(rp.Target.EffectiveUser)
					userFlag := ""
					if userArg != "" {
						userFlag = " --user " + shellEscapeForSh(userArg)
					}

					// Run the wrapper in tmux mode so it behaves like the rest of the TUI workflows.
					wrapperLine := shellEscapeForSh(bin) + " ssh --tmux --host " + shellEscapeForSh(hostKey) + userFlag
					_ = exec.Command("tmux", "send-keys", "-t", targetPane, "--", "bash -lc "+shellQuoteCmdSimple([]string{wrapperLine}), "Enter").Run()
					// Delay after starting SSH before sending on_connect/pane commands.
					// Use per-pane delay if configured; otherwise default to 500ms.
					delayMS := rp.EffectiveConnectDelayMS
					if delayMS <= 0 {
						delayMS = 500
					}
					time.Sleep(time.Duration(delayMS) * time.Millisecond)

					// Send pane-level commands (EffectiveOnConnect + pane.Commands already merged)
					// and log them with copy/paste-friendly markers.
					m.sendRecorded(rp.Target.Host.Name, targetPane, rp.EffectiveCommands)
				}

				// If opened in a new window, provide quick navigation hints without requiring ":" commands.
				if createNewWindow && windowID != "" {
					_ = exec.Command("tmux", "display-message", "-d", "3500", "Dashboard window created. Switch with: n/p (next/prev) or choose-window (prefix+w).").Run()
				}

				m.setStatus(fmt.Sprintf("Materialized dashboard: %s", d.Name), 2000)
				m.showDashBrowser = false
				m.pendingG = false
				m.input.Blur()
				m.recomputeFilter()
				return m, tea.ClearScreen
			default:
				// ignore other keys in dashboard mode
				return m, nil
			}
		}
		// Help overlay: most keys exit help
		if m.showHelp {
			switch msg.String() {
			case "?", "esc", "enter", "q":
				m.showHelp = false
				m.pendingG = false
				m.input.Blur()
				m.recomputeFilter()
				return m, tea.ClearScreen
			default:
				m.showHelp = false
				// fallthrough and handle normally
			}
		}

		// ":" command bar has priority (SecureCRT-like)
		if m.showCmdline && m.cmdline.Focused() {
			switch msg.String() {
			case "esc":
				m.showCmdline = false
				m.cmdline.SetValue("")
				m.cmdline.Blur()
				m.cmdSuggestIdx = -1
				m.pendingG = false
				return m, tea.ClearScreen

			case "tab":
				// Simple completion:
				// - If there's an unambiguous first suggestion, fill it.
				// - Otherwise cycle through up to 10 suggestions.
				if m.cmdCandidates == nil {
					m.cmdCandidates = m.buildCmdCandidates()
				}
				cur := m.cmdline.Value()
				sugs := m.cmdSuggestions(cur)
				if len(sugs) == 0 {
					return m, nil
				}
				m.cmdSuggestIdx++
				if m.cmdSuggestIdx >= len(sugs) {
					m.cmdSuggestIdx = 0
				}
				m.cmdline.SetValue(sugs[m.cmdSuggestIdx])
				return m, nil

			case "enter":
				raw := strings.TrimSpace(m.cmdline.Value())
				m.showCmdline = false
				m.cmdline.SetValue("")
				m.cmdline.Blur()
				m.cmdSuggestIdx = -1
				m.pendingG = false

				// Parse: first token + remainder.
				fields := strings.Fields(raw)
				cmd := ""
				rest := ""
				if len(fields) > 0 {
					cmd = strings.ToLower(fields[0])
					if len(fields) > 1 {
						rest = strings.TrimSpace(raw[len(fields[0]):])
					}
				}

				// Helpers
				openDashBrowser := func() {
					m.input.Blur()
					m.showDashBrowser = true
					if m.dashSelected < 0 {
						m.dashSelected = 0
					}
				}
				setQuery := func(q string) {
					m.input.SetValue(strings.TrimSpace(q))
					m.recomputeFilter()
				}

				switch cmd {
				case "", "menu":
					// Full reference lives here (footer is intentionally minimal).
					m.setStatus(
						"Navigation: j/k move • gg/G top/bot • u/d half-page • H/L group • n/N next/prev match\n"+
							"Search: / forward • ? backward • type to search • Esc blur • :search <q>\n"+
							"Connect: Enter/c connect • v split • s split • w window • W windows (all selected)\n"+
							"Selection: Space toggle-select • f favorite • F favorites • R recents • A all\n"+
							"Dashboards: B browser • :dash [name] • l layout override (in dashboards)\n"+
							"Network: N view • :net • :network\n"+
							"Commands: :send <cmd> • :sendall <cmd> • :watch [interval_s] <cmd> • :watchall [interval_s] <cmd>\n"+
							"Save: Ctrl+s save current window as a recorded dashboard\n"+
							"Recorder: :record start <name> [desc...] • :record stop • :record save [name] • :record delete <name> • :record status\n"+
							"Host: S settings • :login manual|askpass|status • :cred status|set|delete\n"+
							"Logs: l viewer • O pager • T toggle logging policy • :logs • :log toggle|on|off\n"+
							"SSH config: e edit selected host in ~/.ssh/config • E export selector • I import selector • :ssh export • :ssh import\n"+
							"Other: y yank • Y yank-all • :run <macro> [split v|split h|window|connect] • q quit\n"+
							"tmux: prefix+w choose-window • prefix+n/p next/prev",
						12000,
					)
					return m, tea.ClearScreen

				case "h", "help":
					m.showHelp = true
					return m, tea.ClearScreen

				case "q", "quit", "exit":
					m.quitting = true
					return m.quit()

				case "ssh":
					// :ssh export -> open SSH export selector (from ~/.ssh/config)
					// :ssh import -> open SSH import selector (from ~/.ssh/config)
					//
					// NOTE: The actual export/import actions are triggered from within the modal
					// (currently placeholders), but this wires the command bar entry points.
					sub := strings.ToLower(strings.TrimSpace(rest))
					if strings.HasPrefix(sub, "export") {
						entries, err := LoadSSHConfigDefault()
						if err != nil {
							m.setStatus(fmt.Sprintf("ssh export: load ~/.ssh/config failed: %v", err), 3500)
							return m, tea.ClearScreen
						}
						m.showSSHConfigXfer = true
						m.sshXferMode = "export"
						m.sshXferEntries = entries
						m.sshXferFilteredIdx = nil
						m.sshXferSelectedSet = make(map[int]struct{})
						m.sshXferSelectedCursor = 0
						m.sshXferScroll = 0
						m.sshXferQuery.SetValue("")
						m.sshXferQuery.Blur()
						m.sshXferStatus = "Space/Enter select • ctrl+a all • ctrl+d clear • / filter • q close"
						m.input.Blur()
						m.showHelp = false
						m.pendingG = false
						m.numBuf = ""
						return m, tea.ClearScreen
					}
					if strings.HasPrefix(sub, "import") {
						entries, err := LoadSSHConfigDefault()
						if err != nil {
							m.setStatus(fmt.Sprintf("ssh import: load ~/.ssh/config failed: %v", err), 3500)
							return m, tea.ClearScreen
						}
						m.showSSHConfigXfer = true
						m.sshXferMode = "import"
						m.sshXferEntries = entries
						m.sshXferFilteredIdx = nil
						m.sshXferSelectedSet = make(map[int]struct{})
						m.sshXferSelectedCursor = 0
						m.sshXferScroll = 0
						m.sshXferQuery.SetValue("")
						m.sshXferQuery.Blur()
						m.sshXferStatus = "Space/Enter select • ctrl+a all • ctrl+d clear • / filter • q close"
						m.input.Blur()
						m.showHelp = false
						m.pendingG = false
						m.numBuf = ""
						return m, tea.ClearScreen
					}
					m.setStatus("Usage: :ssh export | :ssh import", 3500)
					return m, tea.ClearScreen

				case "search", "/":
					m.searchForward = true
					setQuery(rest)
					m.input.Focus()
					return m, tea.ClearScreen

				case "?":
					m.searchForward = false
					setQuery(rest)
					m.input.Focus()
					return m, tea.ClearScreen

				case "clear":
					setQuery("")
					return m, tea.ClearScreen

				case "all":
					m.filterFavorites = false
					m.filterRecents = false
					m.recomputeFilter()
					return m, tea.ClearScreen

				case "fav", "favorites":
					m.filterFavorites = !m.filterFavorites
					m.filterRecents = false
					m.recomputeFilter()
					return m, tea.ClearScreen

				case "recent", "recents":
					m.filterRecents = !m.filterRecents
					m.filterFavorites = false
					m.recomputeFilter()
					return m, tea.ClearScreen

				case "dashsave":
					// backward-compat alias for `:dash save ...`
					cmd = "dash"
					rest = "save " + strings.TrimSpace(rest)
					// fallthrough to dash handler below
					fallthrough

				case "watch":
					// :watch [interval_s] <command...>
					// Sugar over :send that wraps the command in `watch -n <interval> -t -- <cmd>`.
					args := strings.Fields(strings.TrimSpace(rest))
					if len(args) == 0 {
						m.setStatus("Usage: :watch [interval_s] <command>", 3500)
						return m, tea.ClearScreen
					}
					interval := 2
					restCmd := strings.TrimSpace(rest)
					if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
						interval = n
						restCmd = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), args[0]))
					}
					if strings.TrimSpace(restCmd) == "" {
						m.setStatus("Usage: :watch [interval_s] <command>", 3500)
						return m, tea.ClearScreen
					}
					wrapped := fmt.Sprintf("watch -n %d -t -- %s", interval, strings.TrimSpace(restCmd))

					// Reuse :send implementation (inline, kept explicit for clarity).
					// Resolve target pane: prefer current pane, else last created pane.
					paneID := ""
					if out, e := exec.Command("tmux", "display-message", "-p", "#{pane_id}").Output(); e == nil {
						paneID = strings.TrimSpace(string(out))
					}
					if paneID == "" && len(m.createdPaneIDs) > 0 {
						paneID = strings.TrimSpace(m.createdPaneIDs[len(m.createdPaneIDs)-1])
					}
					if paneID == "" {
						m.setStatus("watch: could not resolve a tmux pane id", 3500)
						return m, tea.ClearScreen
					}
					hostKey := ""
					if m.paneHost != nil {
						hostKey = strings.TrimSpace(m.paneHost[paneID])
					}
					if hostKey == "" {
						// fallback: if current selection exists, attribute to it
						if sel := m.current(); sel != nil {
							hostKey = strings.TrimSpace(sel.Resolved.Host.Name)
						}
					}
					if hostKey == "" {
						m.setStatus("watch: unknown host for target pane (open via tmux-ssh-manager first)", 4500)
						return m, tea.ClearScreen
					}
					m.sendRecorded(hostKey, paneID, []string{wrapped})
					m.setStatus(fmt.Sprintf("watch every %ds: sent + recorded", interval), 2000)
					return m, tea.ClearScreen

				case "watchall":
					// :watchall [interval_s] <command...>
					// Sugar over :sendall that wraps the command in `watch -n <interval> -t -- <cmd>`.
					args := strings.Fields(strings.TrimSpace(rest))
					if len(args) == 0 {
						m.setStatus("Usage: :watchall [interval_s] <command>", 3500)
						return m, tea.ClearScreen
					}
					interval := 2
					restCmd := strings.TrimSpace(rest)
					if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
						interval = n
						restCmd = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), args[0]))
					}
					if strings.TrimSpace(restCmd) == "" {
						m.setStatus("Usage: :watchall [interval_s] <command>", 3500)
						return m, tea.ClearScreen
					}
					wrapped := fmt.Sprintf("watch -n %d -t -- %s", interval, strings.TrimSpace(restCmd))

					if len(m.createdPaneIDs) == 0 {
						m.setStatus("watchall: no panes tracked (use v/s/w/W or dashboards first)", 3500)
						return m, tea.ClearScreen
					}
					sent := 0
					for _, pid := range m.createdPaneIDs {
						pid = strings.TrimSpace(pid)
						if pid == "" {
							continue
						}
						hostKey := ""
						if m.paneHost != nil {
							hostKey = strings.TrimSpace(m.paneHost[pid])
						}
						if hostKey == "" {
							continue
						}
						m.sendRecorded(hostKey, pid, []string{wrapped})
						sent++
					}
					m.setStatus(fmt.Sprintf("watch every %ds: sent + recorded to %d pane(s)", interval, sent), 2500)
					return m, tea.ClearScreen

				case "send":
					// :send <command...>
					// Sends to the current tmux pane (or the most-recently created pane) and records it.
					cmdStr := strings.TrimSpace(rest)
					if cmdStr == "" {
						m.setStatus("Usage: :send <command>", 3500)
						return m, tea.ClearScreen
					}
					// Resolve target pane: prefer current pane, else last created pane.
					paneID := ""
					if out, e := exec.Command("tmux", "display-message", "-p", "#{pane_id}").Output(); e == nil {
						paneID = strings.TrimSpace(string(out))
					}
					if paneID == "" && len(m.createdPaneIDs) > 0 {
						paneID = strings.TrimSpace(m.createdPaneIDs[len(m.createdPaneIDs)-1])
					}
					if paneID == "" {
						m.setStatus("send: could not resolve a tmux pane id", 3500)
						return m, tea.ClearScreen
					}
					hostKey := ""
					if m.paneHost != nil {
						hostKey = strings.TrimSpace(m.paneHost[paneID])
					}
					if hostKey == "" {
						// fallback: if current selection exists, attribute to it
						if sel := m.current(); sel != nil {
							hostKey = strings.TrimSpace(sel.Resolved.Host.Name)
						}
					}
					if hostKey == "" {
						m.setStatus("send: unknown host for target pane (open via tmux-ssh-manager first)", 4500)
						return m, tea.ClearScreen
					}
					m.sendRecorded(hostKey, paneID, []string{cmdStr})
					m.setStatus("sent + recorded", 1500)
					return m, tea.ClearScreen

				case "sendall":
					// :sendall <command...>
					// Sends to all panes created by tmux-ssh-manager (best-effort) and records per pane.
					cmdStr := strings.TrimSpace(rest)
					if cmdStr == "" {
						m.setStatus("Usage: :sendall <command>", 3500)
						return m, tea.ClearScreen
					}
					if len(m.createdPaneIDs) == 0 {
						m.setStatus("sendall: no panes tracked (use v/s/w/W or dashboards first)", 3500)
						return m, tea.ClearScreen
					}
					sent := 0
					for _, pid := range m.createdPaneIDs {
						pid = strings.TrimSpace(pid)
						if pid == "" {
							continue
						}
						hostKey := ""
						if m.paneHost != nil {
							hostKey = strings.TrimSpace(m.paneHost[pid])
						}
						if hostKey == "" {
							continue
						}
						m.sendRecorded(hostKey, pid, []string{cmdStr})
						sent++
					}
					m.setStatus(fmt.Sprintf("sent + recorded to %d pane(s)", sent), 2000)
					return m, tea.ClearScreen

				case "net", "network":
					// Open Network View (LLDP topology) for selected/current.
					// This provides a deterministic escape hatch when N is consumed by search navigation (n/N next/prev match).
					targets := m.selectedResolved()
					if len(targets) == 0 {
						if sel := m.current(); sel != nil {
							targets = []ResolvedHost{sel.Resolved}
						}
					}
					if len(targets) == 0 {
						m.setStatus("network: no target selected", 1500)
						return m, tea.ClearScreen
					}
					m.showNetworkView = true
					m.netLoading = true
					m.netErr = ""
					m.netStatus = "collecting LLDP neighbors..."
					m.netSelected = append([]ResolvedHost(nil), targets...)
					m.netNodes = nil
					m.netEdges = nil
					m.netNodeIndex = make(map[string]int)
					m.netSelectedID = ""
					m.netQueryMode = false
					m.netQuery.SetValue("")
					m.netQuery.Blur()
					return m, tea.Batch(tea.ClearScreen, runLLDPCollectionCmd(m.cfg, targets))

				case "dashboards":
					openDashBrowser()
					return m, tea.ClearScreen

				case "split":
					// :split <N> [v|h|window] [layout]
					// :split v|h                    (legacy)
					//
					// Single-host fanout:
					// Opens N total connections to the currently selected host, using:
					// - v: stacked splits (tmux split-window -v)
					// - h: side-by-side splits (tmux split-window -h)
					// - window: one new tmux window per connection
					//
					// Notes:
					// - This is intentionally single-host only (uses current selection).
					// - Requires tmux.
					args := strings.Fields(strings.TrimSpace(rest))
					if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
						m.setStatus("Usage: :split <N> [v|h|window] [layout]  (or legacy: :split v|h)", 3500)
						return m, tea.ClearScreen
					}

					// Legacy behavior: :split v|h (no count)
					if strings.EqualFold(strings.TrimSpace(args[0]), "v") || strings.EqualFold(strings.TrimSpace(args[0]), "h") {
						// Fall through to legacy handler below by rewriting rest and continuing.
						// We keep this case here so help/usage stays discoverable in one place.
						arg := strings.ToLower(strings.TrimSpace(args[0]))
						targets := m.currentOrSelectedResolved()
						if len(targets) == 0 {
							m.setStatus("No target selected", 1500)
							return m, tea.ClearScreen
						}
						failed := 0
						switch arg {
						case "v":
							for _, r := range targets {
								if _, err := m.tmuxSplitH(r); err != nil {
									failed++
									continue
								}
								m.addRecent(r.Host.Name)
							}
						case "h":
							for _, r := range targets {
								if _, err := m.tmuxSplitV(r); err != nil {
									failed++
									continue
								}
								m.addRecent(r.Host.Name)
							}
						}
						if failed > 0 {
							m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
						}
						m.saveState()
						return m.quit()
					}

					// New behavior: :split <N> [mode] [layout]
					n, err := strconv.Atoi(strings.TrimSpace(args[0]))
					if err != nil || n <= 0 {
						m.setStatus("split: N must be a positive integer (or use legacy: :split v|h)", 3500)
						return m, tea.ClearScreen
					}
					mode := "window"
					if len(args) >= 2 && strings.TrimSpace(args[1]) != "" {
						mode = strings.ToLower(strings.TrimSpace(args[1]))
					}
					// Shorthand aliases
					if mode == "w" {
						mode = "window"
					}
					layout := ""
					if len(args) >= 3 {
						layout = strings.TrimSpace(strings.Join(args[2:], " "))
					}
					if mode != "v" && mode != "h" && mode != "window" {
						m.setStatus("split: mode must be v, h, window (or w)", 3500)
						return m, tea.ClearScreen
					}

					if strings.TrimSpace(os.Getenv("TMUX")) == "" {
						m.setStatus("split: requires tmux", 2500)
						return m, tea.ClearScreen
					}

					sel := m.current()
					if sel == nil {
						m.setStatus("split: no host selected", 2500)
						return m, tea.ClearScreen
					}
					r := sel.Resolved

					// For N==1, behave like default "enter": new window connect (in tmux).
					if n == 1 {
						if _, err := m.tmuxNewWindow(r); err != nil {
							_, _ = m.tmuxSplitV(r)
						}
						m.addRecent(r.Host.Name)
						m.saveState()
						return m.quit()
					}

					switch mode {
					case "window":
						failed := 0
						for i := 0; i < n; i++ {
							if _, err := m.tmuxNewWindow(r); err != nil {
								failed++
								continue
							}
							m.addRecent(r.Host.Name)
						}
						if failed > 0 {
							m.setStatus(fmt.Sprintf("Opened %d, %d failed", n-failed, failed), 2500)
						}
						m.saveState()
						return m.quit()

					case "v", "h":
						// Create a safe new window for the first connection, then split within it.
						windowID, err := m.tmuxNewWindow(r)
						if err != nil {
							// fallback: if new window fails, do the first connect as a split in current window
							// and continue splitting there.
							windowID = ""
						}
						m.addRecent(r.Host.Name)

						failed := 0
						for i := 1; i < n; i++ {
							if mode == "h" {
								if _, err := m.tmuxSplitH(r); err != nil {
									failed++
									continue
								}
							} else {
								if _, err := m.tmuxSplitV(r); err != nil {
									failed++
									continue
								}
							}
							m.addRecent(r.Host.Name)
						}

						if strings.TrimSpace(layout) != "" {
							// If we created a dedicated window, apply the layout there; otherwise apply to current.
							target := strings.TrimSpace(windowID)
							if target == "" {
								target = "#{window_id}"
							}
							_ = exec.Command("tmux", "select-layout", "-t", target, strings.TrimSpace(layout)).Run()
						}

						if failed > 0 {
							m.setStatus(fmt.Sprintf("Opened %d, %d failed", n-failed, failed), 2500)
						}
						m.saveState()
						return m.quit()
					}

					return m, tea.ClearScreen

				case "dash", "dashboard":
					// :dash -> open browser
					// :dash <name> -> open browser + preselect match
					// :dash save <name> [desc...] -> snapshot current tmux window into a recorded dashboard
					// :dash export <name> [yaml|json] -> export dashboard to tmux-session-manager spec (no apply)
					// :dash apply <name> -> export + apply via tmux-session-manager (if available)
					// :dash apply-file <path> -> apply an existing spec file via tmux-session-manager
					// Matches both YAML dashboards and recorded dashboards (state.json).
					name := strings.TrimSpace(rest)

					// Subcommand: export dashboard to tmux-session-manager spec file
					// Usage: :dash export <name> [yaml|json]
					if strings.HasPrefix(strings.ToLower(name), "export ") {
						args := strings.Fields(strings.TrimSpace(name))
						if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
							m.setStatus("Usage: :dash export <name> [yaml|json]", 3500)
							return m, tea.ClearScreen
						}
						dashName := strings.TrimSpace(args[1])
						format := "yaml"
						if len(args) >= 3 {
							f := strings.ToLower(strings.TrimSpace(args[2]))
							if f == "json" {
								format = "json"
							}
						}

						if m.cfg == nil {
							m.setStatus("dash export: config not loaded (need hosts/groups to resolve panes)", 3500)
							return m, tea.ClearScreen
						}

						// Find dashboard in config or recorded dashboards
						var d Dashboard
						found := false
						if m.cfg != nil {
							if dd := m.cfg.FindDashboard(dashName); dd != nil {
								d = *dd
								found = true
							}
						}
						if !found && m.state != nil {
							if rdash := m.state.FindRecordedDashboard(dashName); rdash != nil {
								d = rdash.ToConfigDashboard()
								found = true
							}
						}
						if !found {
							m.setStatus(fmt.Sprintf("dash export: dashboard not found: %s", dashName), 3500)
							return m, tea.ClearScreen
						}

						resolved, rerr := m.cfg.ResolveDashboard(d)
						if rerr != nil {
							m.setStatus(fmt.Sprintf("dash export: resolve failed: %v", rerr), 3500)
							return m, tea.ClearScreen
						}

						cfgDir, derr := DefaultConfigDir()
						if derr != nil {
							m.setStatus(fmt.Sprintf("dash export: config dir error: %v", derr), 3500)
							return m, tea.ClearScreen
						}
						outDir := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_SPEC_DIR"))
						if outDir == "" {
							outDir = filepath.Join(cfgDir, "dashboards")
						}
						_ = os.MkdirAll(outDir, 0o700)

						// Deterministic splits default on (pane_plan)
						deterministic := true
						if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS")) == "0" {
							deterministic = false
						}

						exp := sessionfmt.DashboardExport{
							Name:        strings.TrimSpace(d.Name),
							Description: strings.TrimSpace(d.Description),
							Layout:      strings.TrimSpace(d.Layout),
							Panes:       make([]sessionfmt.DashboardPane, 0, len(resolved.Panes)),
						}
						for _, rp := range resolved.Panes {
							hostKey := strings.TrimSpace(rp.Target.Host.Name)
							cmds := make([]string, 0, len(rp.EffectiveCommands))
							for _, c := range rp.EffectiveCommands {
								c = strings.TrimSpace(c)
								if c != "" {
									cmds = append(cmds, c)
								}
							}
							exp.Panes = append(exp.Panes, sessionfmt.DashboardPane{
								Title:    strings.TrimSpace(rp.Pane.Title),
								Host:     hostKey,
								Commands: cmds,
								Env:      nil,
							})
						}

						outPath, pathErr := sessionfmt.SuggestedDashboardSpecPath(outDir, d.Name, sessionfmt.OutputFormat(format))
						if pathErr != nil {
							outPath = filepath.Join(outDir, "dashboard.tmux-session."+format)
						}

						wopt := sessionfmt.WriterOptions{
							SessionName:    "",
							ExportName:     exp.Name,
							Description:    exp.Description,
							Root:           outDir,
							PreferPanePlan: deterministic,
							Layout:         exp.Layout,
							WindowName:     "dashboard",
							PaneTitles:     true,
							ShellProgram:   "bash",
							ShellFlag:      "-lc",
							Now:            time.Now,
							Attach:         boolPtr(true),
							SwitchClient:   boolPtr(true),
						}

						if werr := sessionfmt.WriteDashboardSpec(outPath, sessionfmt.OutputFormat(format), exp, wopt); werr != nil {
							m.setStatus(fmt.Sprintf("dash export failed: %v", werr), 3500)
							return m, tea.ClearScreen
						}

						m.setStatus(fmt.Sprintf("dashboard exported: %s", outPath), 3500)
						return m, tea.ClearScreen
					}

					// Subcommand: apply dashboard by name via tmux-session-manager (export + apply)
					// Usage: :dash apply <name>
					if strings.HasPrefix(strings.ToLower(name), "apply ") {
						args := strings.Fields(strings.TrimSpace(name))
						if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
							m.setStatus("Usage: :dash apply <name>", 3500)
							return m, tea.ClearScreen
						}
						dashName := strings.TrimSpace(args[1])

						bin := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_BIN"))
						if bin == "" {
							bin = "tmux-session-manager"
						}

						// Reuse export logic first (defaults to yaml).
						// Then call tmux-session-manager --spec <file> --spec-session <derived>
						if m.cfg == nil {
							m.setStatus("dash apply: config not loaded (need hosts/groups to resolve panes)", 3500)
							return m, tea.ClearScreen
						}

						// Find dashboard in config or recorded dashboards
						var d Dashboard
						found := false
						if m.cfg != nil {
							if dd := m.cfg.FindDashboard(dashName); dd != nil {
								d = *dd
								found = true
							}
						}
						if !found && m.state != nil {
							if rdash := m.state.FindRecordedDashboard(dashName); rdash != nil {
								d = rdash.ToConfigDashboard()
								found = true
							}
						}
						if !found {
							m.setStatus(fmt.Sprintf("dash apply: dashboard not found: %s", dashName), 3500)
							return m, tea.ClearScreen
						}

						resolved, rerr := m.cfg.ResolveDashboard(d)
						if rerr != nil {
							m.setStatus(fmt.Sprintf("dash apply: resolve failed: %v", rerr), 3500)
							return m, tea.ClearScreen
						}

						cfgDir, derr := DefaultConfigDir()
						if derr != nil {
							m.setStatus(fmt.Sprintf("dash apply: config dir error: %v", derr), 3500)
							return m, tea.ClearScreen
						}
						outDir := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_SPEC_DIR"))
						if outDir == "" {
							outDir = filepath.Join(cfgDir, "dashboards")
						}
						_ = os.MkdirAll(outDir, 0o700)

						// Deterministic splits default on (pane_plan)
						deterministic := true
						if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS")) == "0" {
							deterministic = false
						}

						format := "yaml"
						exp := sessionfmt.DashboardExport{
							Name:        strings.TrimSpace(d.Name),
							Description: strings.TrimSpace(d.Description),
							Layout:      strings.TrimSpace(d.Layout),
							Panes:       make([]sessionfmt.DashboardPane, 0, len(resolved.Panes)),
						}
						for _, rp := range resolved.Panes {
							hostKey := strings.TrimSpace(rp.Target.Host.Name)
							cmds := make([]string, 0, len(rp.EffectiveCommands))
							for _, c := range rp.EffectiveCommands {
								c = strings.TrimSpace(c)
								if c != "" {
									cmds = append(cmds, c)
								}
							}
							exp.Panes = append(exp.Panes, sessionfmt.DashboardPane{
								Title:    strings.TrimSpace(rp.Pane.Title),
								Host:     hostKey,
								Commands: cmds,
								Env:      nil,
							})
						}

						outPath, pathErr := sessionfmt.SuggestedDashboardSpecPath(outDir, d.Name, sessionfmt.OutputFormat(format))
						if pathErr != nil {
							outPath = filepath.Join(outDir, "dashboard.tmux-session."+format)
						}

						wopt := sessionfmt.WriterOptions{
							SessionName:    "",
							ExportName:     exp.Name,
							Description:    exp.Description,
							Root:           outDir,
							PreferPanePlan: deterministic,
							Layout:         exp.Layout,
							WindowName:     "dashboard",
							PaneTitles:     true,
							ShellProgram:   "bash",
							ShellFlag:      "-lc",
							Now:            time.Now,
							Attach:         boolPtr(true),
							SwitchClient:   boolPtr(true),
						}
						if werr := sessionfmt.WriteDashboardSpec(outPath, sessionfmt.OutputFormat(format), exp, wopt); werr != nil {
							m.setStatus(fmt.Sprintf("dash apply: export failed: %v", werr), 3500)
							return m, tea.ClearScreen
						}

						// Apply via tmux-session-manager in a new window.
						// Force a stable session name for dashboards.
						specSession := "dash-" + strings.TrimSpace(sanitizeNameForSession(d.Name))
						_ = exec.Command(
							"tmux",
							"new-window",
							"-n", "dash-apply",
							"-c", "#{pane_current_path}",
							"--",
							"bash",
							"-lc",
							fmt.Sprintf("%s --spec %q --spec-session %q", shellEscapeForSh(bin), outPath, specSession),
						).Run()

						m.setStatus(fmt.Sprintf("dashboard apply requested: %s", d.Name), 2500)
						return m, tea.ClearScreen
					}

					// Subcommand: apply an existing spec file via tmux-session-manager
					// Usage: :dash apply-file <path>
					if strings.HasPrefix(strings.ToLower(name), "apply-file ") {
						rest2 := strings.TrimSpace(name[len("apply-file "):])
						if rest2 == "" {
							m.setStatus("Usage: :dash apply-file <path>", 3500)
							return m, tea.ClearScreen
						}
						specPath := rest2

						bin := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_BIN"))
						if bin == "" {
							bin = "tmux-session-manager"
						}

						_ = exec.Command(
							"tmux",
							"new-window",
							"-n", "dash-apply",
							"-c", "#{pane_current_path}",
							"--",
							"bash",
							"-lc",
							fmt.Sprintf("%s --spec %q", shellEscapeForSh(bin), specPath),
						).Run()

						m.setStatus(fmt.Sprintf("apply-file requested: %s", specPath), 2500)
						return m, tea.ClearScreen
					}

					// Subcommand: save snapshot
					if strings.HasPrefix(strings.ToLower(name), "save ") {
						args := strings.Fields(strings.TrimSpace(name))
						// args[0] == "save"
						if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
							m.setStatus("Usage: :dash save <name> [description...]", 3500)
							return m, tea.ClearScreen
						}
						recName := strings.TrimSpace(args[1])
						desc := ""
						if len(args) > 2 {
							desc = strings.TrimSpace(strings.Join(args[2:], " "))
						}

						// Resolve current window id + layout
						windowID := ""
						if out, e := exec.Command("tmux", "display-message", "-p", "#{window_id}").Output(); e == nil {
							windowID = strings.TrimSpace(string(out))
						}
						if windowID == "" {
							m.setStatus("dash save: could not resolve tmux window id", 3500)
							return m, tea.ClearScreen
						}

						layout := ""
						if out, e := exec.Command("tmux", "display-message", "-p", "-t", windowID, "#{window_layout}").Output(); e == nil {
							layout = strings.TrimSpace(string(out))
						}

						// Snapshot panes in this window. We only include panes we can map to a host.
						panesOut, e := exec.Command("tmux", "list-panes", "-t", windowID, "-F", "#{pane_id}").Output()
						if e != nil {
							m.setStatus(fmt.Sprintf("dash save: list-panes failed: %v", e), 3500)
							return m, tea.ClearScreen
						}
						lines := strings.Split(strings.TrimSpace(string(panesOut)), "\n")

						panes := make([]RecordedPane, 0, len(lines))
						seenHost := map[string]struct{}{}

						for _, ln := range lines {
							pid := strings.TrimSpace(ln)
							if pid == "" {
								continue
							}

							hostKey := ""
							if m.paneHost != nil {
								hostKey = strings.TrimSpace(m.paneHost[pid])
							}
							// If we can't map this pane to a host, skip it (it wasn't created/managed by us).
							if hostKey == "" {
								continue
							}

							seenHost[hostKey] = struct{}{}

							// Pull any recorded commands for this pane (best-effort). If none, empty.
							cmds := []string(nil)
							if m.recordedPanes != nil {
								if rp := m.recordedPanes[pid]; rp != nil && len(rp.Commands) > 0 {
									cmds = append([]string(nil), rp.Commands...)
								}
							}

							panes = append(panes, RecordedPane{
								Title:    "",
								Host:     hostKey,
								Commands: cmds,
							})
						}

						if len(panes) == 0 {
							m.setStatus("dash save: no managed panes with known hosts in this window (open via tmux-ssh-manager first)", 5000)
							return m, tea.ClearScreen
						}

						if m.state == nil {
							m.state = &State{Version: 1}
						}
						rd := RecordedDashboard{
							Name:        recName,
							Description: desc,
							Layout:      layout,
							Panes:       panes,
						}
						_ = m.state.UpsertRecordedDashboard(rd)
						m.saveState()

						// Optional enhancement: also export a tmux-session-manager spec for this dashboard save.
						// This is best-effort and does not affect the existing JSON state save.
						//
						// Enable with:
						//   TMUX_SSH_MANAGER_EXPORT_DASHBOARD_SPEC=1
						//
						// Optional knobs:
						//   TMUX_SSH_MANAGER_DASH_SPEC_FORMAT=yaml|json
						//   TMUX_SSH_MANAGER_DASH_SPEC_DIR=...
						//   TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS=1
						if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_EXPORT_DASHBOARD_SPEC")) != "" {
							cfgDir, derr := DefaultConfigDir()
							if derr == nil {
								outDir := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_SPEC_DIR"))
								if outDir == "" {
									outDir = filepath.Join(cfgDir, "dashboards")
								}

								format := strings.ToLower(strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_SPEC_FORMAT")))
								if format != "json" {
									format = "yaml"
								}

								deterministic := true
								if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_DASH_DETERMINISTIC_SPLITS")) == "0" {
									deterministic = false
								}

								// Convert recorded dashboard back to a config dashboard for export, then resolve.
								cd := rd.ToConfigDashboard()
								resolved, rerr := m.cfg.ResolveDashboard(cd)
								if rerr == nil {
									exp := sessionfmt.DashboardExport{
										Name:        strings.TrimSpace(cd.Name),
										Description: strings.TrimSpace(cd.Description),
										Layout:      strings.TrimSpace(cd.Layout),
										Panes:       make([]sessionfmt.DashboardPane, 0, len(resolved.Panes)),
									}
									for _, rp := range resolved.Panes {
										hostKey := strings.TrimSpace(rp.Target.Host.Name)
										cmds := make([]string, 0, len(rp.EffectiveCommands))
										for _, c := range rp.EffectiveCommands {
											c = strings.TrimSpace(c)
											if c != "" {
												cmds = append(cmds, c)
											}
										}
										exp.Panes = append(exp.Panes, sessionfmt.DashboardPane{
											Title:    strings.TrimSpace(rp.Pane.Title),
											Host:     hostKey,
											Commands: cmds,
											Env:      nil,
										})
									}

									_ = os.MkdirAll(outDir, 0o700)
									outPath, pathErr := sessionfmt.SuggestedDashboardSpecPath(outDir, cd.Name, sessionfmt.OutputFormat(format))
									if pathErr != nil {
										// Best-effort: fall back to a simple filename in outDir.
										outPath = filepath.Join(outDir, "dashboard.tmux-session."+format)
									}

									wopt := sessionfmt.WriterOptions{
										SessionName:    "",
										ExportName:     exp.Name,
										Description:    exp.Description,
										Root:           "",
										PreferPanePlan: deterministic,
										Layout:         exp.Layout,
										WindowName:     "dashboard",
										PaneTitles:     true,
										ShellProgram:   "bash",
										ShellFlag:      "-lc",
										Now:            time.Now,
										Attach:         boolPtr(true),
										SwitchClient:   boolPtr(true),
									}
									_ = sessionfmt.WriteDashboardSpec(outPath, sessionfmt.OutputFormat(format), exp, wopt)
								}
							}
						}

						if layout != "" {
							m.setStatus(fmt.Sprintf("dashboard saved: %s (layout captured, %d pane(s))", recName, len(panes)), 4000)
						} else {
							m.setStatus(fmt.Sprintf("dashboard saved: %s (%d pane(s))", recName, len(panes)), 3500)
						}
						return m, tea.ClearScreen
					}

					// Normal behavior: open browser (and optional preselect)
					openDashBrowser()
					if name != "" {
						allDash := make([]Dashboard, 0, len(m.cfg.Dashboards)+8)
						if m.cfg != nil && len(m.cfg.Dashboards) > 0 {
							allDash = append(allDash, m.cfg.Dashboards...)
						}
						if m.state != nil && len(m.state.RecordedDashboards) > 0 {
							for _, rdd := range m.state.RecordedDashboards {
								allDash = append(allDash, rdd.ToConfigDashboard())
							}
						}
						for i := range allDash {
							if strings.EqualFold(allDash[i].Name, name) {
								m.dashSelected = i
								break
							}
						}
					}
					return m, tea.ClearScreen

				case "connect", "c":
					// Reuse Enter/c behavior by directly invoking the same logic:
					// create window(s) for selected/current then quit.
					targets := m.currentOrSelectedResolved()
					if len(targets) == 0 {
						m.setStatus("No target selected", 1500)
						return m, tea.ClearScreen
					}
					failed := 0
					for _, r := range targets {
						if _, err := m.tmuxNewWindow(r); err != nil {
							if _, serr := m.tmuxSplitV(r); serr != nil {
								failed++
								continue
							}
						}
						m.addRecent(r.Host.Name)
					}
					if failed > 0 {
						m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
					}
					m.saveState()
					return m.quit()

				case "window", "w":
					// Alias to "connect in new window(s)"
					targets := m.currentOrSelectedResolved()
					if len(targets) == 0 {
						m.setStatus("No target selected", 1500)
						return m, tea.ClearScreen
					}
					failed := 0
					for _, r := range targets {
						if _, err := m.tmuxNewWindow(r); err != nil {
							failed++
							continue
						}
						m.addRecent(r.Host.Name)
					}
					if failed > 0 {
						m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
					}
					m.saveState()
					return m.quit()

				case "windows":
					// Can't intercept keys once you're inside SSH; point to tmux-native chooser.
					m.setStatus("Window switching: use tmux prefix+w (choose-window), prefix+n/p next/prev", 4500)
					return m, tea.ClearScreen

				case "logs":
					if sel := m.current(); sel != nil {
						m.pendingG = false
						if err := m.openLogs(sel.Resolved.Host.Name); err != nil {
							m.setStatus(fmt.Sprintf("logs: %v", err), 3500)
							return m, tea.ClearScreen
						}
						m.showLogs = true
						return m, tea.ClearScreen
					}
					m.setStatus("No current host for logs", 1500)
					return m, tea.ClearScreen

				case "cred":
					// cred status|set|delete (system credential store)
					sub := strings.ToLower(strings.TrimSpace(rest))
					switch sub {
					case "status", "test":
						if sel := m.current(); sel != nil {
							hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
							loginMode := strings.ToLower(strings.TrimSpace(m.effectiveLoginMode(sel.Resolved)))

							// Password auth only for now; account defaults to hostKey when user is empty.
							if err := CredGet(hostKey, sel.Resolved.EffectiveUser, "password"); err != nil {
								if loginMode == "askpass" {
									m.setStatus(fmt.Sprintf("cred: MISSING for %s (login_mode=askpass). Run :cred set", hostKey), 4500)
								} else {
									m.setStatus(fmt.Sprintf("cred: missing/unavailable for %s (login_mode=%s)", hostKey, loginMode), 3000)
								}
							} else {
								m.setStatus(fmt.Sprintf("cred: available for %s (login_mode=%s)", hostKey, loginMode), 2500)
							}
							return m, tea.ClearScreen
						}
						m.setStatus("cred: no current host", 1500)
						return m, tea.ClearScreen

					case "set":
						if sel := m.current(); sel != nil {
							hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
							if hostKey == "" {
								m.setStatus("cred: no current host", 1500)
								return m, tea.ClearScreen
							}

							userArg := strings.TrimSpace(sel.Resolved.EffectiveUser)
							bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
							if bin == "" {
								bin = "tmux-ssh-manager"
							}

							userFlag := ""
							if userArg != "" {
								userFlag = "--user " + shellEscapeForSh(userArg)
							}

							// Prefer a foreground popup. -E closes automatically on success.
							backendLabel := CredentialBackendLabel()

							popupCmd := fmt.Sprintf(
								"printf '\033[?25h\033[0m' >/dev/tty 2>/dev/null || true; stty sane </dev/tty >/dev/tty 2>/dev/null || true; %s cred set --host %s %s; rc=$?; echo; if [ $rc -eq 0 ]; then echo 'Saved to system credential store (%s).'; exit 0; else echo 'FAILED (exit='$rc')'; echo; echo 'Press Enter to close...'; read -r _; exit $rc; fi",
								shellEscapeForSh(bin),
								shellEscapeForSh(hostKey),
								userFlag,
								shellEscapeForSh(backendLabel),
							)

							// When already inside a tmux popup, nested popups can render “behind” the current popup.
							// Target the current pane so the credential prompt replaces the existing popup content.
							args := []string{
								"display-popup",
								"-E",
								"-T", fmt.Sprintf("tmux-ssh-manager: credential for %s", hostKey),
								"-w", "80%",
								"-h", "60%",
							}
							if os.Getenv("TMUX_SSH_MANAGER_IN_POPUP") != "" {
								if out, e := exec.Command("tmux", "display-message", "-p", "#{pane_id}").Output(); e == nil {
									pid := strings.TrimSpace(string(out))
									if pid != "" {
										args = append(args, "-t", pid)
									}
								}
							}
							args = append(args, "--", "bash", "-lc", popupCmd)
							popupErr := exec.Command("tmux", args...).Run()

							if popupErr != nil {
								// Fallback: open a window for credential prompting; keep it open until the user confirms.
								_ = exec.Command(
									"tmux", "new-window",
									"-n", "cred-set",
									"bash", "-lc",
									fmt.Sprintf(
										"printf '\033[?25h\033[0m' >/dev/tty 2>/dev/null || true; stty sane </dev/tty >/dev/tty 2>/dev/null || true; %s cred set --host %s %s; rc=$?; echo; if [ $rc -eq 0 ]; then echo 'Saved to system credential store (%s).'; else echo 'FAILED (exit='$rc')'; fi; echo; echo 'Press Enter to close...'; read -r _",
										shellEscapeForSh(bin),
										shellEscapeForSh(hostKey),
										userFlag,
										shellEscapeForSh(backendLabel),
									),
								).Run()
							}

							// Refresh status (non-revealing)
							if err := CredGet(hostKey, sel.Resolved.EffectiveUser, "password"); err != nil {
								m.setStatus("cred: not set (or unavailable)", 3000)
							} else {
								// UX tweak: if a credential was just set and login mode is still manual,
								// automatically enable askpass by persisting auth_mode=keychain in HostExtras.
								if strings.EqualFold(strings.TrimSpace(m.effectiveLoginMode(sel.Resolved)), "manual") {
									ex, _ := LoadHostExtras(hostKey)
									ex.HostKey = hostKey
									ex.AuthMode = "keychain"
									_ = SaveHostExtras(ex)
									m.setStatus(fmt.Sprintf("cred: stored in system credential store (%s) • login mode: askpass enabled", CredentialBackendLabel()), 2500)
								} else {
									m.setStatus(fmt.Sprintf("cred: stored in system credential store (%s)", CredentialBackendLabel()), 2000)
								}
							}
							return m, tea.ClearScreen
						}
						m.setStatus("cred: no current host", 1500)
						return m, tea.ClearScreen

					case "delete", "del", "rm":
						if sel := m.current(); sel != nil {
							hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
							if hostKey == "" {
								m.setStatus("cred: no current host", 1500)
								return m, tea.ClearScreen
							}
							if err := CredDelete(hostKey, sel.Resolved.EffectiveUser, "password"); err != nil {
								m.setStatus(fmt.Sprintf("cred delete failed: %v", err), 3500)
							} else {
								// After deleting the credential, also disable askpass automation for this host.
								// This keeps behavior consistent with Host Settings delete (and avoids a confusing
								// state where login_mode=askpass but no credential exists).
								ex, _ := LoadHostExtras(hostKey)
								ex.HostKey = hostKey
								ex.AuthMode = "manual"
								_ = SaveHostExtras(ex)

								m.setStatus(fmt.Sprintf("cred: deleted from system credential store (%s) • login mode: manual", CredentialBackendLabel()), 2500)
							}
							return m, tea.ClearScreen
						}
						m.setStatus("cred: no current host", 1500)
						return m, tea.ClearScreen

					default:
						m.setStatus("Usage: :cred status|set|delete", 2500)
						return m, tea.ClearScreen
					}

				case "login":
					// login manual|askpass|status (persisted via HostExtras auth_mode; no YAML rewrite)
					arg := strings.ToLower(strings.TrimSpace(rest))
					if sel := m.current(); sel != nil {
						hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
						if hostKey == "" {
							m.setStatus("login: no current host", 1500)
							return m, tea.ClearScreen
						}
						ex, _ := LoadHostExtras(hostKey)
						ex.HostKey = hostKey
						switch arg {
						case "", "status":
							m.setStatus(fmt.Sprintf("login mode: %s", m.effectiveLoginMode(sel.Resolved)), 2500)
							return m, tea.ClearScreen
						case "askpass", "keychain":
							ex.AuthMode = "keychain"
						case "manual":
							ex.AuthMode = "manual"
						default:
							m.setStatus("Usage: :login manual|askpass|status", 2500)
							return m, tea.ClearScreen
						}
						if err := SaveHostExtras(ex); err != nil {
							m.setStatus(fmt.Sprintf("login mode save failed: %v", err), 3500)
							return m, tea.ClearScreen
						}
						m.setStatus(fmt.Sprintf("login mode: %s", m.effectiveLoginMode(sel.Resolved)), 2000)
						return m, tea.ClearScreen
					}
					m.setStatus("login: no current host", 1500)
					return m, tea.ClearScreen

				case "log":
					// log toggle|on|off (policy)
					sub := strings.ToLower(strings.TrimSpace(rest))
					if sel := m.current(); sel != nil {
						hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
						ex, _ := LoadHostExtras(hostKey)
						ex.HostKey = hostKey

						switch sub {
						case "toggle":
							ex.Logging = !ex.Logging
						case "on", "enable", "enabled", "true":
							ex.Logging = true
						case "off", "disable", "disabled", "false":
							ex.Logging = false
						default:
							m.setStatus("Usage: :log toggle|on|off", 2500)
							return m, tea.ClearScreen
						}

						if err := SaveHostExtras(ex); err != nil {
							m.setStatus(fmt.Sprintf("Logging toggle failed: %v", err), 3500)
							return m, tea.ClearScreen
						}
						if ex.Logging {
							m.setStatus("Logging: enabled", 1500)
						} else {
							m.setStatus("Logging: disabled", 1500)
						}
						return m, tea.ClearScreen
					}
					m.setStatus("No current host for log policy", 1500)
					return m, tea.ClearScreen

				case "run":
					// :run <macroName> [split v|h|window|connect]
					// Default: window (one per target) because it's safest and least disruptive.
					args := strings.Fields(strings.TrimSpace(rest))
					if len(args) == 0 {
						m.setStatus("Usage: :run <macro> [split v|h|window|connect]", 3500)
						return m, tea.ClearScreen
					}
					macroName := args[0]
					mode := "window"
					if len(args) > 1 {
						mode = strings.ToLower(strings.Join(args[1:], " "))
					}

					mac := m.findMacro(macroName)
					if mac == nil {
						m.setStatus(fmt.Sprintf("Unknown macro: %s", macroName), 3500)
						return m, tea.ClearScreen
					}

					targets := m.currentOrSelectedResolved()
					if len(targets) == 0 {
						m.setStatus("No target selected", 1500)
						return m, tea.ClearScreen
					}

					// We create panes/windows and then send macro commands with logging markers.
					// This is best-effort "send after connect": we start SSH and then delay briefly
					// before sending macro commands via tmux send-keys into the target pane.
					failed := 0
					sent := 0
					for _, r := range targets {
						var paneID string
						var err error

						switch mode {
						case "connect":
							// Same as default connect (new window) for safety.
							fallthrough
						case "window":
							paneID, err = m.tmuxNewWindow(r)
						case "split v":
							paneID, err = m.tmuxSplitH(r)
						case "split h":
							paneID, err = m.tmuxSplitV(r)
						default:
							m.setStatus("Usage: :run <macro> [split v|h|window|connect]", 3500)
							return m, tea.ClearScreen
						}

						if err != nil {
							failed++
							continue
						}
						m.addRecent(r.Host.Name)

						// Track mapping so :send/:dash save can work later.
						if strings.TrimSpace(paneID) != "" {
							if m.paneHost == nil {
								m.paneHost = make(map[string]string)
							}
							m.paneHost[strings.TrimSpace(paneID)] = strings.TrimSpace(r.Host.Name)
						}

						// If we have a target pane id, delay after starting SSH before sending macro commands,
						// then send and log them.
						if strings.TrimSpace(paneID) != "" {
							// Use effective per-host delay if configured; otherwise default to 500ms.
							delayMS := r.EffectiveConnectDelayMS
							if delayMS <= 0 {
								delayMS = 500
							}
							time.Sleep(time.Duration(delayMS) * time.Millisecond)

							m.sendRecorded(r.Host.Name, paneID, mac.Commands)
							sent++
						}
					}

					if failed > 0 {
						m.setStatus(fmt.Sprintf("Opened %d, %d failed • Macro sent to %d pane(s)", len(targets)-failed, failed, sent), 4500)
					} else {
						m.setStatus(fmt.Sprintf("Opened %d • Macro sent to %d pane(s)", len(targets), sent), 3000)
					}

					m.saveState()
					return m.quit()

				case "record":
					// Minimal recorder UI:
					// - :record start <name> [description...]
					// - :record stop
					// - :record status
					// - :record save [name]
					// - :record delete <name>
					subArgs := strings.Fields(strings.TrimSpace(rest))
					sub := ""
					if len(subArgs) > 0 {
						sub = strings.ToLower(strings.TrimSpace(subArgs[0]))
					}
					switch sub {
					case "", "status":
						if m.recording {
							m.setStatus(fmt.Sprintf("recording: ON (%s)", strings.TrimSpace(m.recordingName)), 3500)
						} else {
							m.setStatus("recording: OFF", 2500)
						}
						return m, tea.ClearScreen

					case "start":
						// record start <name> [desc...]
						if len(subArgs) < 2 || strings.TrimSpace(subArgs[1]) == "" {
							m.setStatus("Usage: :record start <name> [description...]", 3500)
							return m, tea.ClearScreen
						}
						name := strings.TrimSpace(subArgs[1])
						desc := ""
						if len(subArgs) > 2 {
							desc = strings.TrimSpace(strings.Join(subArgs[2:], " "))
						}
						m.recording = true
						m.recordingName = name
						m.recordingDescription = desc
						m.recordedPanes = make(map[string]*RecordedPane)
						m.setStatus(fmt.Sprintf("recording started: %s", name), 3000)
						return m, tea.ClearScreen

					case "stop":
						if !m.recording {
							m.setStatus("recording: already stopped", 2000)
							return m, tea.ClearScreen
						}
						m.recording = false
						m.setStatus("recording stopped (use :record save)", 3000)
						return m, tea.ClearScreen

					case "save":
						// record save [name]
						name := strings.TrimSpace(m.recordingName)
						if len(subArgs) >= 2 && strings.TrimSpace(subArgs[1]) != "" {
							name = strings.TrimSpace(subArgs[1])
						}
						if name == "" {
							m.setStatus("record save: missing name (use :record start <name> or :record save <name>)", 4500)
							return m, tea.ClearScreen
						}
						if m.state == nil {
							m.state = &State{Version: 1}
						}
						// Turn recorded panes into deterministic slice.
						panes := make([]RecordedPane, 0, len(m.recordedPanes))
						for _, rp := range m.recordedPanes {
							if rp == nil {
								continue
							}
							if strings.TrimSpace(rp.Host) == "" {
								continue
							}
							// Keep only non-empty commands.
							cmds := rp.Commands[:0]
							for _, c := range rp.Commands {
								c = strings.TrimSpace(c)
								if c != "" {
									cmds = append(cmds, c)
								}
							}
							rp.Commands = cmds
							panes = append(panes, *rp)
						}
						if len(panes) == 0 {
							m.setStatus("record save: nothing to save yet (only records commands the manager sends)", 4500)
							return m, tea.ClearScreen
						}
						rd := RecordedDashboard{
							Name:        name,
							Description: strings.TrimSpace(m.recordingDescription),
							Panes:       panes,
						}
						_ = m.state.UpsertRecordedDashboard(rd)
						m.saveState()
						m.setStatus(fmt.Sprintf("recorded dashboard saved: %s", name), 3500)
						return m, tea.ClearScreen

					case "delete", "del", "rm":
						if len(subArgs) < 2 || strings.TrimSpace(subArgs[1]) == "" {
							m.setStatus("Usage: :record delete <name>", 3500)
							return m, tea.ClearScreen
						}
						name := strings.TrimSpace(subArgs[1])
						if m.state == nil {
							m.state = &State{Version: 1}
						}
						if m.state.DeleteRecordedDashboard(name) {
							m.saveState()
							m.setStatus(fmt.Sprintf("recorded dashboard deleted: %s", name), 3000)
						} else {
							m.setStatus(fmt.Sprintf("record delete: not found: %s", name), 3500)
						}
						return m, tea.ClearScreen

					default:
						m.setStatus("Usage: :record start|stop|status|save|delete", 3500)
						return m, tea.ClearScreen
					}

				default:
					m.setStatus(fmt.Sprintf("Unknown command: %s (try :menu)", raw), 3500)
					return m, tea.ClearScreen
				}

			default:
				var tcmd tea.Cmd
				m.cmdline, tcmd = m.cmdline.Update(msg)
				return m, tcmd
			}
		}

		// Insert-mode (search focused) is the default. In this mode:
		// - treat nearly all keys as literal text (so j/k/n/q/s/v/etc. are not stolen)
		// - allow arrow keys to navigate the selection without leaving insert-mode
		// - only allow explicit mode switches or confirmations
		if m.input.Focused() {
			switch msg.String() {
			case "up":
				m.move(-1)
				return m, nil
			case "down":
				m.move(1)
				return m, nil

			case "enter":
				// Confirm current selection while staying in the main flow.
				// Mirror tmux-session-manager behavior: accept even if search is focused.
				m.input.Blur()
				m.recomputeFilter()
				targets := m.selectedResolved()
				if len(targets) == 0 {
					if sel := m.current(); sel != nil {
						targets = []ResolvedHost{sel.Resolved}
					}
				}
				if len(targets) == 0 {
					return m, nil
				}

				// Outside tmux: connect inline (single target only).
				if strings.TrimSpace(os.Getenv("TMUX")) == "" {
					if len(targets) != 1 {
						m.setStatus("Multi-select connect requires tmux (panes/windows). Start tmux or select a single host.", 3500)
						return m, nil
					}
					r := targets[0]
					m.addRecent(r.Host.Name)
					m.saveState()
					return m.connectOrQuit(r)
				}

				// In tmux: default to new window(s), with split fallback (same as main Enter behavior).
				failed := 0
				for _, r := range targets {
					if _, err := m.tmuxNewWindow(r); err != nil {
						if _, serr := m.tmuxSplitV(r); serr != nil {
							failed++
							continue
						}
					}
					m.addRecent(r.Host.Name)
				}
				if failed > 0 {
					m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
				}
				m.saveState()
				return m.quit()

			case "esc":
				// Explicitly exit insert-mode -> Normal-mode (vim mental model).
				// In Normal-mode, j/k/n/q/... work as motions/actions.
				m.input.Blur()
				m.recomputeFilter()
				return m, nil

			case ":":
				// Explicitly enter command mode from insert-mode.
				m.pendingG = false
				m.input.Blur()
				m.showHelp = false
				m.showCmdline = true
				m.cmdSuggestIdx = -1
				if m.cmdCandidates == nil {
					m.cmdCandidates = m.buildCmdCandidates()
				}
				m.cmdline.SetValue("")
				m.cmdline.Focus()
				return m, tea.ClearScreen
			}

			// Default: treat as text input (including q/j/k/n/s/v/etc.)
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)

			// update filter on any change
			m.recomputeFilter()
			return m, cmd
		}

		// Not focused: handle motions, actions, numeric selects
		k := msg.String()

		// Numeric quick-select buffer
		if isDigitKey(k) {
			m.numBuf += k
			m.setStatus(fmt.Sprintf("Number: %s (Enter to select)", m.numBuf), 1500)
			return m, nil
		}
		if k == "enter" && m.numBuf != "" {
			// Connect to given index (1-based)
			idx, _ := strconv.Atoi(m.numBuf)
			m.numBuf = ""
			if idx >= 1 && idx <= len(m.filtered) {
				return m.connectOrQuit(m.filtered[idx-1].Resolved)
			}
			m.setStatus("Invalid index", 1200)
			return m, nil
		}
		if k == "backspace" && m.numBuf != "" {
			if len(m.numBuf) > 0 {
				m.numBuf = m.numBuf[:len(m.numBuf)-1]
			}
			return m, nil
		}
		if k == "esc" && m.numBuf != "" {
			m.numBuf = ""
			return m, nil
		}

		// Motions
		switch k {
		case "j", "down":
			m.move(1)
			return m, nil
		case "k", "up":
			m.move(-1)
			return m, nil
		case "n":
			// Next match (vim): move in the current search direction through the filtered list.
			// Since the list is already filtered by the query, "next match" == next item.
			if m.searchForward {
				m.move(1)
			} else {
				m.move(-1)
			}
			return m, nil
		case "N":
			// Previous match (vim): opposite direction of "n".
			if m.searchForward {
				m.move(-1)
			} else {
				m.move(1)
			}
			return m, nil
		case "g":
			// "gg" to go to top
			if m.pendingG {
				m.pendingG = false
				m.gotoTop()
			} else {
				m.pendingG = true
			}
			return m, nil
		case "G":
			m.pendingG = false
			m.gotoBottom()
			return m, nil
		case "u", "pgup", "ctrl+u":
			// half-page up (vim)
			m.pageUp()
			return m, nil
		case "d", "pgdown", "ctrl+d":
			// half-page down (vim)
			m.pageDown()
			return m, nil
		case "H":
			m.pendingG = false
			m.jumpGroupPrev()
			return m, nil
		case "L":
			m.pendingG = false
			m.jumpGroupNext()
			return m, nil
		}

		// If logs viewer is active, it consumes most navigation keys.
		if m.showLogs {
			handled, m2, cmd := m.handleLogsKeys(msg)
			if handled {
				// handleLogsKeys returns the updated model by value.
				// Return it directly to avoid interface/type assertion pitfalls.
				return *m2, cmd
			}
			// If not handled, fall through to normal actions.
		}

		// Actions
		switch k {
		case "N":
			// Network view: run LLDP collection for selected/current then render an ASCII topology.
			targets := m.selectedResolved()
			if len(targets) == 0 {
				if sel := m.current(); sel != nil {
					targets = []ResolvedHost{sel.Resolved}
				}
			}
			if len(targets) == 0 {
				m.setStatus("network: no target selected", 1500)
				return m, nil
			}
			m.showNetworkView = true
			m.netLoading = true
			m.netErr = ""
			m.netStatus = "collecting LLDP neighbors..."
			m.netSelected = append([]ResolvedHost(nil), targets...)
			m.netNodes = nil
			m.netEdges = nil
			m.netNodeIndex = make(map[string]int)
			m.netSelectedID = ""
			m.netQueryMode = false
			m.netQuery.SetValue("")
			m.netQuery.Blur()
			return m, tea.Batch(tea.ClearScreen, runLLDPCollectionCmd(m.cfg, targets))

		case "enter", "c":
			// If there are multi-selected hosts, operate on all of them.
			// Otherwise, operate on the current host.
			targets := m.selectedResolved()
			if len(targets) == 0 {
				if sel := m.current(); sel != nil {
					targets = []ResolvedHost{sel.Resolved}
				}
			}
			if len(targets) == 0 {
				return m, nil
			}

			// If we're not in tmux, connect inline in the current terminal.
			//
			// Outside tmux, multi-select is disallowed because we can't create additional panes/windows.
			if strings.TrimSpace(os.Getenv("TMUX")) == "" {
				if len(targets) != 1 {
					m.setStatus("Multi-select connect requires tmux (panes/windows). Start tmux or select a single host.", 3500)
					return m, nil
				}
				r := targets[0]
				m.addRecent(r.Host.Name)
				m.saveState()
				return m.connectOrQuit(r)
			}

			// In tmux: connect in new tmux window(s) by default.
			// If new-window fails for any reason, fall back to a vertical split for that target.
			failed := 0
			for _, r := range targets {
				if _, err := m.tmuxNewWindow(r); err != nil {
					// Fallback: split vertically
					if _, serr := m.tmuxSplitV(r); serr != nil {
						failed++
						continue
					}
				}
				m.addRecent(r.Host.Name)
			}

			if failed > 0 {
				m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
			}
			m.saveState()
			return m.quit()
		case "l":
			// Open per-host logs viewer (daily logs under ~/.config/tmux-ssh-manager/logs/<hostkey>/YYYY-MM-DD.log)
			if sel := m.current(); sel != nil {
				m.pendingG = false
				if err := m.openLogs(sel.Resolved.Host.Name); err != nil {
					m.setStatus(fmt.Sprintf("logs: %v", err), 3500)
					return m, nil
				}
				m.showLogs = true
				return m, nil
			}
			return m, nil
		case "O":
			// Open today's per-host log in $PAGER (default: less +F) in a new tmux pane.
			// We sanitize at view-time (strip CR + ANSI/control sequences) so the pager view
			// doesn't show ESC[...] noise from pipe-pane logs.
			if sel := m.current(); sel != nil {
				hostKey := sel.Resolved.Host.Name
				opts := DefaultLogOptions()
				info, err := EnsureDailyHostLog(hostKey, time.Now(), opts)
				if err != nil {
					m.setStatus(fmt.Sprintf("open log: %v", err), 3500)
					return m, nil
				}

				pager := strings.TrimSpace(os.Getenv("PAGER"))
				if pager == "" {
					pager = "less"
				}

				// Follow mode by default for tailing-like experience.
				// Use bash -lc so users can set PAGER as an alias/func if they want.
				//
				// Notes:
				// - `tr -d '\r'` removes ^M from CRLF / carriage-returned prompts.
				// - `perl ...` strips common ANSI escape sequences (CSI/OSC/charset selects).
				// - `less -R` is still fine; after stripping ANSI, it behaves like normal less.
				filter := fmt.Sprintf(
					"cat %s | tr -d '\\r' | perl -pe 's/\\e\\[[0-9;?]*[ -\\/]*[@-~]//g; s/\\e\\][^\\a]*(\\a|\\e\\\\)//g; s/\\e\\([A-Za-z0-9]//g; s/\\e\\][0-9];.*?\\a//g' | %s -R +F",
					shellEscapeForSh(info.Path),
					shellEscapeForSh(pager),
				)

				// Prefer a split pane for viewing.
				_ = exec.Command("tmux", "split-window", "-v", "-c", "#{pane_current_path}", "bash", "-lc", filter).Run()
				m.setStatus("Opened log in pager", 1500)
				return m, nil
			}
			return m, nil
		case "e":
			// Edit the selected host's ssh config at the exact file + line for its Host block.
			//
			// UX goal:
			// - Stay on the main host list (no separate selection screen).
			// - Press `e` to open the *defining* ssh config file and line for the selected alias.
			//
			// Implementation:
			// - Parse OpenSSH config (default loader supports Includes).
			// - Find the first matching literal Host entry for the alias; use its Source + StartLine.
			// - Open `vi +<line> <file>` (popup-aware); otherwise open in a tmux split.
			// - Fallback: open primary ~/.ssh/config if we can't locate a source line.
			if m.showHelp || m.showLogs || m.showDashBrowser || m.showCmdline || m.showHostSettings || m.showAddSSHHost || m.showMergeDupsConfirm || m.showRouterIDEditor || m.showMgmtIPEditor || m.showDeviceOSEditor || m.showNetworkView || m.showSSHConfigXfer || m.showSSHPathPrompt || m.showSSHPostWriteConfirm {
				return m, nil
			}
			sel := m.current()
			if sel == nil {
				m.setStatus("ssh edit: no host selected", 2000)
				return m, nil
			}
			alias := strings.TrimSpace(sel.Resolved.Host.Name)
			if alias == "" {
				m.setStatus("ssh edit: empty host alias", 2000)
				return m, nil
			}

			// Resolve the defining file + line for this alias.
			target := ""
			line := 0
			if entries, err := LoadSSHConfigDefault(); err == nil && len(entries) > 0 {
				for _, e := range entries {
					if strings.TrimSpace(e.Alias) != alias {
						continue
					}
					// Prefer the first (earliest) defining occurrence so the user sees the
					// config block that actually introduced the alias (Includes supported).
					if strings.TrimSpace(e.Source) != "" && e.StartLine > 0 {
						target = strings.TrimSpace(e.Source)
						line = e.StartLine
						break
					}
				}
			}

			// Fallback to primary ~/.ssh/config if we couldn't locate a specific source/line.
			if strings.TrimSpace(target) == "" {
				if p, err := LoadSSHConfigPrimaryPath(); err == nil {
					target = p
				} else {
					target = filepath.Join(os.Getenv("HOME"), ".ssh", "config")
				}
			}
			target = expandUserAndEnv(strings.TrimSpace(target))

			// Hardcode a safe terminal editor.
			editor := "vi"

			// Build an editor invocation that jumps to the line when known.
			argv := []string{}
			if line > 0 {
				argv = []string{fmt.Sprintf("+%d", line), target}
			} else {
				argv = []string{target}
			}

			// Best-effort: restore terminal state before launching the editor.
			restoreTerminalForExec()

			if strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_IN_POPUP")) == "1" {
				cmd := exec.Command(editor, argv...)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				if line > 0 {
					m.setStatus(fmt.Sprintf("ssh edit: opening %s:%d...", target, line), 2000)
				} else {
					m.setStatus(fmt.Sprintf("ssh edit: opening %s...", target), 2000)
				}
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
					if err != nil {
						return errMsg{Err: fmt.Errorf("ssh edit: %w", err)}
					}
					return statusMsg("ssh edit: editor closed")
				})
			}

			// Non-popup: run editor inside a new tmux split.
			// Use a shell command to support `vi +<line> <file>` form cleanly.
			cmdline := ""
			if line > 0 {
				cmdline = fmt.Sprintf("exec %s +%d %q", editor, line, target)
			} else {
				cmdline = fmt.Sprintf("exec %s %q", editor, target)
			}
			cmd := exec.Command("tmux", "split-window", "-v", "-c", "#{pane_current_path}", "bash", "-lc", cmdline)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				m.setStatus(fmt.Sprintf("ssh edit: %v", err), 3500)
				return m, nil
			}
			m.setStatus("ssh edit: editor closed", 1500)
			return m, tea.ClearScreen

		case "S":
			// Host Settings overlay (SecureCRT-like session properties)
			m.pendingG = false
			m.input.Blur()
			m.showHelp = false
			m.showHostSettings = true
			m.hostSettingsSel = 0
			return m, tea.ClearScreen

		case "E":
			// SSH config export selector (from ~/.ssh/config)
			// Opens a modal to multi-select hosts and export a new ssh config containing only those entries.
			if m.showHelp || m.showLogs || m.showDashBrowser || m.showCmdline || m.showHostSettings || m.showAddSSHHost || m.showMergeDupsConfirm || m.showRouterIDEditor || m.showMgmtIPEditor || m.showDeviceOSEditor || m.showNetworkView {
				return m, nil
			}
			entries, err := LoadSSHConfigDefault()
			if err != nil {
				m.setStatus(fmt.Sprintf("ssh export: load ~/.ssh/config failed: %v", err), 3500)
				return m, nil
			}
			m.showSSHConfigXfer = true
			m.sshXferMode = "export"
			m.sshXferEntries = entries
			m.sshXferFilteredIdx = nil
			m.sshXferSelectedSet = make(map[int]struct{})
			m.sshXferSelectedCursor = 0
			m.sshXferScroll = 0
			m.sshXferQuery.SetValue("")
			m.sshXferQuery.Blur()
			m.sshXferStatus = "Space/Enter select • ctrl+a all • ctrl+d clear • / filter • q close"
			m.input.Blur()
			m.showHelp = false
			m.pendingG = false
			m.numBuf = ""
			return m, tea.ClearScreen

		case "I":
			// SSH config import selector (from ~/.ssh/config)
			// Opens a modal to multi-select hosts for import flow.
			if m.showHelp || m.showLogs || m.showDashBrowser || m.showCmdline || m.showHostSettings || m.showAddSSHHost || m.showMergeDupsConfirm || m.showRouterIDEditor || m.showMgmtIPEditor || m.showDeviceOSEditor || m.showNetworkView {
				return m, nil
			}
			entries, err := LoadSSHConfigDefault()
			if err != nil {
				m.setStatus(fmt.Sprintf("ssh import: load ~/.ssh/config failed: %v", err), 3500)
				return m, nil
			}
			m.showSSHConfigXfer = true
			m.sshXferMode = "import"
			m.sshXferEntries = entries
			m.sshXferFilteredIdx = nil
			m.sshXferSelectedSet = make(map[int]struct{})
			m.sshXferSelectedCursor = 0
			m.sshXferScroll = 0
			m.sshXferQuery.SetValue("")
			m.sshXferQuery.Blur()
			m.sshXferStatus = "Space/Enter select • ctrl+a all • ctrl+d clear • / filter • q close"
			m.input.Blur()
			m.showHelp = false
			m.pendingG = false
			m.numBuf = ""
			return m, tea.ClearScreen

		case "T":
			// Toggle per-host logging policy (default ON unless explicitly disabled).
			// Persisted to: ~/.config/tmux-ssh-manager/hosts/<hostkey>.conf as `logging=true|false`.
			if sel := m.current(); sel != nil {
				hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
				ex, _ := LoadHostExtras(hostKey)
				ex.HostKey = hostKey

				// Flip and persist
				ex.Logging = !ex.Logging
				if err := SaveHostExtras(ex); err != nil {
					m.setStatus(fmt.Sprintf("Logging toggle failed: %v", err), 3500)
					return m, nil
				}

				if ex.Logging {
					m.setStatus("Logging: enabled", 1500)
				} else {
					m.setStatus("Logging: disabled", 1500)
				}
				return m, nil
			}
			return m, nil
		case "K":
			// Deprecated: key install moved into Host Settings (S) to make the workflow more discoverable.
			// Use: S (Host Settings) -> "Install my public key (authorized_keys)".
			m.setStatus("Key install moved: press S (Host Settings) and select 'Install my public key'", 3500)
			return m, nil
		case " ":
			// toggle multi-select for current row
			if sel := m.current(); sel != nil {
				if _, ok := m.selectedSet[m.selected]; ok {
					delete(m.selectedSet, m.selected)
				} else {
					m.selectedSet[m.selected] = struct{}{}
				}
				m.setStatus(fmt.Sprintf("Selected: %d", m.selectedCount()), 1200)
			}
			return m, nil
		case "f":
			// toggle favorite on current host
			if sel := m.current(); sel != nil {
				name := sel.Resolved.Host.Name
				if m.isFavorite(name) {
					m.unfavorite(name)
					m.setStatus("Removed from favorites", 1200)
				} else {
					m.favorite(name)
					m.setStatus("Added to favorites", 1200)
				}
				m.recomputeFilter()
				m.saveState()
			}
			return m, nil
		case "F":
			// filter favorites
			m.filterFavorites = !m.filterFavorites
			m.filterRecents = false
			m.recomputeFilter()
			return m, nil
		case "R":
			// filter recents
			m.filterRecents = !m.filterRecents
			m.filterFavorites = false
			m.recomputeFilter()
			return m, nil
		case "A":
			// clear filters
			m.filterFavorites = false
			m.filterRecents = false
			m.recomputeFilter()
			return m, nil
		case "v":
			// If there are multi-selected hosts, split and connect each of them (side-by-side).
			// Otherwise, operate on the current host.
			targets := m.selectedResolved()
			if len(targets) == 0 {
				if sel := m.current(); sel != nil {
					targets = []ResolvedHost{sel.Resolved}
				}
			}
			if len(targets) == 0 {
				return m, nil
			}

			failed := 0
			for _, r := range targets {
				if _, err := m.tmuxSplitH(r); err != nil {
					failed++
					continue
				}
				m.addRecent(r.Host.Name)
			}

			// Optional: after batch split, apply a tiled layout (opt-in; default is sequential splits).
			// Enable with: TMUX_SSH_MANAGER_TILED_AFTER_BATCH_SPLIT=1
			if len(targets) > 1 && strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_TILED_AFTER_BATCH_SPLIT")) != "" &&
				os.Getenv("TMUX_SSH_MANAGER_TILED_AFTER_BATCH_SPLIT") != "0" {
				_ = exec.Command("tmux", "select-layout", "tiled").Run()
			}

			if failed > 0 {
				m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
			}
			m.saveState()
			return m.quit()

		case "s":
			// If there are multi-selected hosts, split and connect each of them (stacked).
			// Otherwise, operate on the current host.
			targets := m.selectedResolved()
			if len(targets) == 0 {
				if sel := m.current(); sel != nil {
					targets = []ResolvedHost{sel.Resolved}
				}
			}
			if len(targets) == 0 {
				return m, nil
			}

			failed := 0
			for _, r := range targets {
				if _, err := m.tmuxSplitV(r); err != nil {
					failed++
					continue
				}
				m.addRecent(r.Host.Name)
			}

			// Optional: after batch split, apply a tiled layout (opt-in; default is sequential splits).
			// Enable with: TMUX_SSH_MANAGER_TILED_AFTER_BATCH_SPLIT=1
			if len(targets) > 1 && strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_TILED_AFTER_BATCH_SPLIT")) != "" &&
				os.Getenv("TMUX_SSH_MANAGER_TILED_AFTER_BATCH_SPLIT") != "0" {
				_ = exec.Command("tmux", "select-layout", "tiled").Run()
			}

			if failed > 0 {
				m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
			}
			m.saveState()
			return m.quit()

		case "w":
			// If there are multi-selected hosts, open a new window for each.
			// Otherwise, operate on the current host.
			targets := m.selectedResolved()
			if len(targets) == 0 {
				if sel := m.current(); sel != nil {
					targets = []ResolvedHost{sel.Resolved}
				}
			}
			if len(targets) == 0 {
				return m, nil
			}

			failed := 0
			for _, r := range targets {
				if _, err := m.tmuxNewWindow(r); err != nil {
					failed++
					continue
				}
				m.addRecent(r.Host.Name)
			}
			if failed > 0 {
				m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
			}
			m.saveState()
			return m.quit()
		case "W":
			// batch: open each selected in a new window (or current if none selected)
			targets := m.selectedResolved()
			if len(targets) == 0 {
				if sel := m.current(); sel != nil {
					targets = []ResolvedHost{sel.Resolved}
				}
			}
			failed := 0
			for _, r := range targets {
				if _, err := m.tmuxNewWindow(r); err != nil {
					failed++
				} else {
					m.addRecent(r.Host.Name)
				}
			}
			if failed > 0 {
				m.setStatus(fmt.Sprintf("Opened %d, %d failed", len(targets)-failed, failed), 2500)
			}
			m.saveState()
			return m.quit()
		case "y":
			if sel := m.current(); sel != nil {
				// Prefer the wrapper form so yanked commands work even if the user has `alias ssh='tmux-ssh-manager ssh'`,
				// and so credential automation behavior is consistent.
				//
				// UX: avoid wrapping the TUI by abbreviating long absolute paths for display/yank:
				// - if TMUX_SSH_MANAGER_BIN is an absolute path, render as "../<basename>"
				// - otherwise keep as-is
				binExec := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
				if binExec == "" {
					binExec = "tmux-ssh-manager"
				}
				binDisp := binExec
				if strings.HasPrefix(binDisp, "/") {
					binDisp = "../" + filepath.Base(binDisp)
				}

				hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
				userArg := strings.TrimSpace(sel.Resolved.EffectiveUser)
				userFlag := ""
				if userArg != "" {
					userFlag = " --user " + shellEscapeForSh(userArg)
				}
				line := shellEscapeForSh(binDisp) + " ssh --tmux --host " + shellEscapeForSh(hostKey) + userFlag
				if err := m.tmuxYank(line); err != nil {
					m.setStatus(fmt.Sprintf("yank error: %v", err), 2500)
					return m, nil
				}
				m.setStatus("Yanked SSH command to tmux buffer", 1200)
			}
			return m, nil
		case "Y":
			// batch yank
			targets := m.selectedResolved()
			if len(targets) == 0 {
				if sel := m.current(); sel != nil {
					targets = []ResolvedHost{sel.Resolved}
				}
			}
			lines := make([]string, 0, len(targets))
			for _, r := range targets {
				// Prefer wrapper form for the same reasons as yank: consistent auth behavior + avoids alias recursion.
				//
				// UX: abbreviate long absolute paths for display/yank:
				// - if TMUX_SSH_MANAGER_BIN is an absolute path, render as "../<basename>"
				// - otherwise keep as-is
				binExec := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
				if binExec == "" {
					binExec = "tmux-ssh-manager"
				}
				binDisp := binExec
				if strings.HasPrefix(binDisp, "/") {
					binDisp = "../" + filepath.Base(binDisp)
				}

				hostKey := strings.TrimSpace(r.Host.Name)
				userArg := strings.TrimSpace(r.EffectiveUser)
				userFlag := ""
				if userArg != "" {
					userFlag = " --user " + shellEscapeForSh(userArg)
				}
				lines = append(lines, shellEscapeForSh(binDisp)+" ssh --tmux --host "+shellEscapeForSh(hostKey)+userFlag)
			}
			if err := m.tmuxYank(strings.Join(lines, "\n")); err != nil {
				m.setStatus(fmt.Sprintf("yank error: %v", err), 2500)
			} else {
				m.setStatus(fmt.Sprintf("Yanked %d commands", len(lines)), 1200)
			}
			return m, nil
		case "/":
			// Search forward (vim-ish). This focuses the search input and sets direction for n/N.
			m.pendingG = false
			m.searchForward = true
			m.cmdline.Blur()
			m.showCmdline = false
			m.input.Focus()
			return m, nil
		case "?":
			// Search backward (vim-ish). Focus the search input and set direction for n/N.
			// Do NOT mutate the query string; direction is tracked separately.
			m.pendingG = false
			m.searchForward = false
			m.cmdline.Blur()
			m.showCmdline = false
			m.input.Focus()
			return m, nil
		case ":":
			// SecureCRT-like command bar
			m.pendingG = false
			m.input.Blur()
			m.showHelp = false
			m.showCmdline = true
			m.cmdSuggestIdx = -1
			if m.cmdCandidates == nil {
				m.cmdCandidates = m.buildCmdCandidates()
			}
			m.cmdline.SetValue("")
			m.cmdline.Focus()
			return m, nil
		case "h":
			// Preserve single-key help as a convenience, but steer users toward :help.
			m.pendingG = false
			m.showHelp = true
			return m, nil
		case "B":
			// Open dashboards browser
			m.pendingG = false
			m.input.Blur()
			m.showDashBrowser = true
			if m.dashSelected < 0 {
				m.dashSelected = 0
			}
			return m, nil
		}

		// Treat other typed text as new search query (vim-ish)
		if len(k) == 1 && k != " " {
			m.input.Focus()
			m.input.SetValue(m.input.Value() + k)
			m.recomputeFilter()
			return m, nil
		}
		return m, nil

	}

	return m, nil
}

func (m *model) handleGlobalKeys(k tea.KeyMsg) (handled bool, quit bool) {
	switch k.String() {
	case "ctrl+c":
		// Important: Bubble Tea must receive tea.Quit, otherwise the program can
		// appear to "hang" in tmux popups because the process is still running.
		m.quitting = true
		return true, true

	case "q":
		// Consistent rule:
		// - If ANY modal/overlay is active, let that layer handle "q" as close/cancel.
		// - Only quit the entire program when no modal/overlay is active.
		if m.showHelp ||
			m.showLogs ||
			m.showDashBrowser ||
			m.showCmdline ||
			m.showHostSettings ||
			m.showAddSSHHost ||
			m.showMergeDupsConfirm ||
			m.showRouterIDEditor ||
			m.showMgmtIPEditor ||
			m.showDeviceOSEditor ||
			m.showNetworkView {
			return false, false
		}
		m.quitting = true
		return true, true

	case "M":
		// Merge duplicate Host blocks for the selected alias in primary ~/.ssh/config.
		// This now requires explicit confirmation (modal).
		if m.showHelp || m.showLogs || m.showDashBrowser || m.showCmdline || m.showHostSettings || m.showMergeDupsConfirm {
			return false, false
		}
		sel := m.current()
		if sel == nil {
			m.setStatus("merge: no host selected", 1500)
			return true, false
		}
		alias := strings.TrimSpace(sel.Resolved.Host.Name)
		if alias == "" {
			m.setStatus("merge: empty host alias", 1500)
			return true, false
		}
		n := 0
		if m.dupAliasCount != nil {
			n = m.dupAliasCount[alias]
		}
		if n < 2 {
			m.setStatus("merge: no duplicates for selected alias in ~/.ssh/config", 2500)
			return true, false
		}

		m.showMergeDupsConfirm = true
		m.mergeDupsAlias = alias
		m.mergeDupsCount = n
		m.mergeDupsConfirmHint = "This will modify ~/.ssh/config (a .bak will be written)."
		m.input.Blur()
		return true, false

	case "ctrl+s":
		// Save current window as a recorded dashboard (hotkey).
		// This is intended for NOC workflows: build panes, :watchall ..., resize, then ctrl+s to save.
		//
		// Note: we avoid a dedicated prompt here; it saves under an auto-generated name.
		// Users can later rename by editing state.json if desired.
		if m.showHelp || m.showLogs || m.showCmdline || m.showHostSettings {
			return false, false
		}
		if m.showDashBrowser {
			// Let dashboard browser own ctrl+s in the future if desired.
			return false, false
		}

		// Resolve current window id + layout
		windowID := ""
		if out, e := exec.Command("tmux", "display-message", "-p", "#{window_id}").Output(); e == nil {
			windowID = strings.TrimSpace(string(out))
		}
		if windowID == "" {
			m.setStatus("save: could not resolve tmux window id", 3500)
			return true, false
		}

		layout := ""
		if out, e := exec.Command("tmux", "display-message", "-p", "-t", windowID, "#{window_layout}").Output(); e == nil {
			layout = strings.TrimSpace(string(out))
		}

		// Snapshot panes in this window. We only include panes we can map to a host.
		panesOut, e := exec.Command("tmux", "list-panes", "-t", windowID, "-F", "#{pane_id}").Output()
		if e != nil {
			m.setStatus(fmt.Sprintf("save: list-panes failed: %v", e), 3500)
			return true, false
		}
		lines := strings.Split(strings.TrimSpace(string(panesOut)), "\n")

		panes := make([]RecordedPane, 0, len(lines))
		for _, ln := range lines {
			pid := strings.TrimSpace(ln)
			if pid == "" {
				continue
			}
			hostKey := ""
			if m.paneHost != nil {
				hostKey = strings.TrimSpace(m.paneHost[pid])
			}
			// If we can't map this pane to a host, skip it (it wasn't created/managed by us).
			if hostKey == "" {
				continue
			}

			// Pull any recorded commands for this pane (best-effort). If none, empty.
			cmds := []string(nil)
			if m.recordedPanes != nil {
				if rp := m.recordedPanes[pid]; rp != nil && len(rp.Commands) > 0 {
					cmds = append([]string(nil), rp.Commands...)
				}
			}

			panes = append(panes, RecordedPane{
				Title:    "",
				Host:     hostKey,
				Commands: cmds,
			})
		}

		if len(panes) == 0 {
			m.setStatus("save: no managed panes with known hosts in this window (open via tmux-ssh-manager first)", 5000)
			return true, false
		}

		if m.state == nil {
			m.state = &State{Version: 1}
		}

		// Auto-name: noc-YYYYmmdd-HHMMSS (local time)
		recName := "noc-" + time.Now().Format("20060102-150405")
		rd := RecordedDashboard{
			Name:        recName,
			Description: "saved from ctrl+s",
			Layout:      layout,
			Panes:       panes,
		}
		_ = m.state.UpsertRecordedDashboard(rd)
		m.saveState()

		if layout != "" {
			m.setStatus(fmt.Sprintf("dashboard saved: %s (layout captured, %d pane(s))", recName, len(panes)), 4000)
		} else {
			m.setStatus(fmt.Sprintf("dashboard saved: %s (%d pane(s))", recName, len(panes)), 3500)
		}
		return true, false

	case "esc":
		// esc clears modal/help/log view or search focus
		if m.showHelp {
			m.showHelp = false
			m.pendingG = false
			m.input.Blur()
			m.recomputeFilter()
			return true, false
		}
		if m.showLogs {
			// Close logs view and force a clean redraw on next tick.
			m.showLogs = false
			m.logHostKey = ""
			m.logFilePath = ""
			m.logFiles = nil
			m.logSelected = 0
			m.logStartLine = 0
			m.logLines = nil
			m.logTotalLines = 0
			m.pendingG = false
			m.input.Blur()
			m.recomputeFilter()
			return true, false
		}
		if m.showDashBrowser {
			m.showDashBrowser = false
			m.pendingG = false
			m.input.Blur()
			m.recomputeFilter()
			return true, false
		}

		if m.input.Focused() {
			m.input.Blur()
			m.recomputeFilter()
			return true, false
		}
	}
	return false, false
}

// --- View ---

func (m model) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "tmux-ssh-manager: loading...\n"
	}

	var b strings.Builder

	// Header (keep short; put details in help)
	headerLeft := "tmux-ssh-manager — Sessions"
	b.WriteString(m.theme.HeaderLine(headerLeft) + "\n")
	b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", minInt(len(headerLeft), maxInt(3, m.width)))) + "\n")

	// Network view overlay (LLDP topology)
	if m.showNetworkView {
		b.WriteString(m.theme.HeaderLine("Network View — LLDP Topology") + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 55)))) + "\n")

		if m.netLoading {
			b.WriteString("\n")
			st := strings.TrimSpace(m.netStatus)
			if st == "" {
				st = "collecting..."
			}
			b.WriteString("Status: " + st + "\n")
			b.WriteString("\n")
			b.WriteString("Tip: collection runs in the background; results will appear automatically.\n")
			b.WriteString("\n")
			b.WriteString("Keys: Esc/q close\n")
			return b.String()
		}

		if strings.TrimSpace(m.netErr) != "" {
			b.WriteString("\n")
			b.WriteString("Warning: " + m.netErr + "\n")
		}
		b.WriteString("\n")
		mode := strings.ToLower(strings.TrimSpace(m.netViewMode))
		switch mode {
		case "edges":
			b.WriteString(renderNetEdgesView(m.width, m.height, m.netNodes, m.netEdges, m.netSelectedID, m.netUseBoxDrawing))
		case "list":
			b.WriteString(renderNetASCII(m.width, m.height, m.netNodes, m.netEdges, m.netSelectedID))
		default:
			b.WriteString(renderNetLayered(m.width, m.height, m.netNodes, m.netEdges, m.netSelectedID, m.netUseBoxDrawing))
		}
		b.WriteString("\n")

		if n := m.netFocusedNode(); n != nil {
			b.WriteString("\n")
			title := n.DisplayName
			if title == "" {
				title = n.ID
			}
			if n.Configured {
				b.WriteString("Selected: " + title + " [configured]\n")
			} else {
				b.WriteString("Selected: " + title + " [unknown]\n")
			}
			if n.Downregulated && n.Configured {
				b.WriteString("Note: configured host but no LLDP links (island)\n")
			}
			if strings.TrimSpace(n.IdentityHint) != "" {
				b.WriteString("Identity: " + n.IdentityHint + "\n")
			}
			if n.Resolved != nil {
				b.WriteString("SSH target: " + n.Resolved.Host.Name + "\n")
			}
		}

		b.WriteString("\n")
		if m.netQueryMode {
			b.WriteString(m.netQuery.View() + "\n")
			b.WriteString("Keys: Enter search • Esc cancel\n")
		} else {
			b.WriteString("Keys: j/k or Tab move • Enter/c connect • v split-v • s split-h • / search • r refresh • Esc/q close\n")
		}
		return b.String()
	}

	// Router-ID editor modal view (HostExtras.router_id)
	if m.showRouterIDEditor {
		b.WriteString(m.theme.HeaderLine("Router-ID (topology matching)") + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 55)))) + "\n")
		b.WriteString("\n")
		b.WriteString(m.routerIDInput.View() + "\n")
		b.WriteString("\n")
		b.WriteString("Network View (when open):\n")
		b.WriteString("Keys: j/k or Tab: move focus • /: search/jump • r: refresh\n")
		b.WriteString("  Enter/c: connect (configured node only) • v: split-v (tmux) • s: split-h (tmux)\n")
		b.WriteString("  m: cycle view (layered/edges/list) • b: toggle box drawing\n")
		b.WriteString("  Esc/q: close network view\n\n")
		b.WriteString("Logs:\n")
		b.WriteString("  - Identity hint for LLDP parsing (typically a loopback).\n")
		b.WriteString("  - NOT used as the SSH destination.\n")
		b.WriteString("\n")
		b.WriteString("Keys: Enter save • Esc/q cancel\n")
		return b.String()
	}

	// Mgmt IP editor modal view (HostExtras.mgmt_ip)
	if m.showMgmtIPEditor {
		b.WriteString(m.theme.HeaderLine("Mgmt IP (topology matching)") + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 55)))) + "\n")
		b.WriteString("\n")
		b.WriteString(m.mgmtIPInput.View() + "\n")
		b.WriteString("\n")
		b.WriteString("Notes:\n")
		b.WriteString("  - Identity hint for LLDP parsing (typically a management address).\n")
		b.WriteString("  - NOT used as the SSH destination.\n")
		b.WriteString("\n")
		b.WriteString("Keys: Enter save • Esc/q cancel\n")
		return b.String()
	}

	// Device OS editor modal view (HostExtras.device_os)
	if m.showDeviceOSEditor {
		b.WriteString(m.theme.HeaderLine("Device OS (topology discovery)") + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 55)))) + "\n")
		b.WriteString("\n")
		b.WriteString(m.deviceOSInput.View() + "\n")
		b.WriteString("\n")
		b.WriteString("Supported values:\n")
		b.WriteString("  - cisco_iosxe\n")
		b.WriteString("  - sonic_dell\n")
		b.WriteString("\n")
		b.WriteString("Notes:\n")
		b.WriteString("  - Stored locally per-host as HostExtras.device_os (does not modify YAML).\n")
		b.WriteString("  - Used by Network View to choose neighbor discovery commands/parsers.\n")
		b.WriteString("\n")
		b.WriteString("Keys: Enter save • Esc/q cancel\n")
		return b.String()
	}

	// Host Settings overlay view
	if m.showHostSettings {
		b.WriteString(m.theme.HeaderLine("Host Settings") + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 40)))) + "\n")

		// Always render the action list, even when there are no hosts yet.
		items := []string{
			"Toggle login mode (manual/askpass)",
			"Set credential (system store)",
			"Delete credential (system store)",
			"Toggle logging",
			"Install my public key (authorized_keys)",
			"Add SSH Host (append to ~/.ssh/config)",
			"Set Router-ID (topology matching)",
			"Set Mgmt IP (topology matching)",
			"Set Neighbor Discovery (auto/lldp/cdp)",
			"Set Device OS (topology discovery)",
		}
		for i, it := range items {
			prefix := "   "
			if i == m.hostSettingsSel {
				prefix = " > "
			}
			b.WriteString(fmt.Sprintf("%s%2d) %s\n", prefix, i+1, it))
		}

		b.WriteString("\n")

		if sel := m.current(); sel != nil {
			hostKey := strings.TrimSpace(sel.Resolved.Host.Name)
			loginMode := m.effectiveLoginMode(sel.Resolved)

			credStatus := "unknown"
			if err := CredGet(hostKey, sel.Resolved.EffectiveUser, "password"); err == nil {
				credStatus = "available"
			} else {
				credStatus = "missing"
			}

			logPolicy := "on"
			disc := "auto"
			if ex, err := LoadHostExtras(hostKey); err == nil {
				if ex.Logging {
					logPolicy = "on"
				} else {
					logPolicy = "off"
				}
				// Best-effort: show the current neighbor discovery preference if present.
				// This is stored in per-host extras as `neighbor_discovery=auto|lldp|cdp`.
				if v := strings.TrimSpace(strings.ToLower(ex.NeighborDiscovery)); v != "" {
					disc = v
				}
			}

			lines := []string{
				fmt.Sprintf("Host: %s", hostKey),
				fmt.Sprintf("Login mode: %s", loginMode),
				fmt.Sprintf("Credential: %s", credStatus),
				fmt.Sprintf("Logging: %s", logPolicy),
				fmt.Sprintf("Neighbor discovery: %s", disc),
			}
			for _, ln := range lines {
				b.WriteString(ln + "\n")
			}
		} else {
			b.WriteString("No host selected.\n")
			b.WriteString("Tip: choose 'Add SSH Host' to create ~/.ssh/config and add your first entry.\n")
		}

		numHint := ""
		if strings.TrimSpace(m.numBuf) != "" {
			numHint = fmt.Sprintf(" • Num: %s", m.numBuf)
		}
		b.WriteString("\nKeys: j/k move • gg/G top/bot • 1-9 then Enter select • Enter apply • Esc/q close" + numHint + "\n")
		return b.String()
	}

	// Post-write confirm modal (open editor?)
	if m.showSSHPostWriteConfirm {
		title := "SSH"
		if strings.TrimSpace(m.sshPostWriteAction) == "export" {
			title = "SSH Export"
		} else if strings.TrimSpace(m.sshPostWriteAction) == "import" {
			title = "SSH Import"
		}
		b.WriteString(m.theme.HeaderLine(title+" - Review") + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 70)))) + "\n")
		b.WriteString(fmt.Sprintf("Wrote: %s\n\n", strings.TrimSpace(m.sshPostWritePath)))
		b.WriteString(m.theme.apply(m.theme.Dim, "Open in editor now? (Enter/y = yes, q/esc = no)") + "\n")
		return b.String()
	}

	// SSH path prompt modal
	if m.showSSHPathPrompt {
		title := "SSH Path"
		if strings.TrimSpace(m.sshPathMode) == "export" {
			title = "SSH Export Path"
		} else if strings.TrimSpace(m.sshPathMode) == "import" {
			title = "SSH Import Path"
		}
		b.WriteString(m.theme.HeaderLine(title) + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 70)))) + "\n")
		if strings.TrimSpace(m.sshPathHint) != "" {
			b.WriteString(m.theme.apply(m.theme.Dim, m.sshPathHint) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%s\n\n", m.sshPathInput.View()))
		b.WriteString(m.theme.apply(m.theme.Dim, "Enter confirm • Esc/q cancel • absolute or relative (cwd)") + "\n")
		return b.String()
	}

	// SSH config import/export selector modal
	if m.showSSHConfigXfer {
		mode := strings.ToLower(strings.TrimSpace(m.sshXferMode))
		title := "SSH Config"
		if mode == "export" {
			title = "SSH Config Export (select hosts from ~/.ssh/config)"
		} else if mode == "import" {
			title = "SSH Config Import (select hosts from ~/.ssh/config)"
		} else {
			title = "SSH Config (select hosts)"
		}

		b.WriteString(m.theme.HeaderLine(title) + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 70)))) + "\n")

		// Optional status/help line (non-empty means caller set a hint)
		if strings.TrimSpace(m.sshXferStatus) != "" {
			b.WriteString(m.theme.apply(m.theme.Dim, m.sshXferStatus) + "\n")
		}

		// Filter line
		q := strings.TrimSpace(m.sshXferQuery.Value())
		filterLine := "Filter: "
		if q == "" {
			filterLine += "(none)  (press / to filter, Esc to focus/blur)"
		} else {
			filterLine += q
		}
		b.WriteString(m.theme.apply(m.theme.Dim, filterLine) + "\n\n")

		// Build filtered index (case-insensitive substring match against alias and hostname)
		m.sshXferFilteredIdx = m.sshXferFilteredIdx[:0]
		lq := strings.ToLower(q)
		for i, e := range m.sshXferEntries {
			alias := strings.TrimSpace(e.Alias)
			hn := strings.TrimSpace(e.HostName)
			if alias == "" {
				continue
			}
			if lq == "" {
				m.sshXferFilteredIdx = append(m.sshXferFilteredIdx, i)
				continue
			}
			if strings.Contains(strings.ToLower(alias), lq) || (hn != "" && strings.Contains(strings.ToLower(hn), lq)) {
				m.sshXferFilteredIdx = append(m.sshXferFilteredIdx, i)
			}
		}

		total := len(m.sshXferEntries)
		visible := len(m.sshXferFilteredIdx)
		b.WriteString(m.theme.apply(m.theme.Dim, fmt.Sprintf("Hosts: %d total • %d shown • %d selected", total, visible, len(m.sshXferSelectedSet))) + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 70)))) + "\n")

		// Clamp cursor
		maxN := visible
		if maxN < 0 {
			maxN = 0
		}
		if maxN == 0 {
			b.WriteString(m.theme.apply(m.theme.Dim, "No hosts match the current filter.") + "\n")
			return b.String()
		}
		if m.sshXferSelectedCursor < 0 {
			m.sshXferSelectedCursor = 0
		}
		if m.sshXferSelectedCursor > maxN-1 {
			m.sshXferSelectedCursor = maxN - 1
		}

		// Simple scroll window
		listMax := maxInt(6, m.height-10)
		if listMax < 3 {
			listMax = 3
		}
		if m.sshXferScroll < 0 {
			m.sshXferScroll = 0
		}
		if m.sshXferSelectedCursor < m.sshXferScroll {
			m.sshXferScroll = m.sshXferSelectedCursor
		}
		if m.sshXferSelectedCursor >= m.sshXferScroll+listMax {
			m.sshXferScroll = m.sshXferSelectedCursor - listMax + 1
		}
		if m.sshXferScroll > maxN-1 {
			m.sshXferScroll = maxN - 1
		}
		end := m.sshXferScroll + listMax
		if end > maxN {
			end = maxN
		}

		for row := m.sshXferScroll; row < end; row++ {
			idx := m.sshXferFilteredIdx[row]
			if idx < 0 || idx >= len(m.sshXferEntries) {
				continue
			}
			e := m.sshXferEntries[idx]
			alias := strings.TrimSpace(e.Alias)
			hn := strings.TrimSpace(e.HostName)

			cur := "  "
			if row == m.sshXferSelectedCursor {
				cur = "> "
			}
			sel := "[ ]"
			if _, ok := m.sshXferSelectedSet[idx]; ok {
				sel = "[x]"
			}

			line := fmt.Sprintf("%s%s %s", cur, sel, alias)
			if hn != "" && hn != alias {
				line += m.theme.apply(m.theme.Dim, "  ("+hn+")")
			}
			// Mark duplicates in primary config if present (best-effort hint)
			if m.dupAliasCount != nil {
				if n, ok := m.dupAliasCount[alias]; ok && n > 1 {
					line += m.theme.apply(m.theme.Warn, fmt.Sprintf("  [dups:%d]", n))
				}
			}

			if row == m.sshXferSelectedCursor {
				b.WriteString(m.theme.apply(m.theme.Selected, line) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}

		b.WriteString("\n")
		b.WriteString(m.theme.apply(m.theme.Dim, "Keys: j/k move • Space/Enter toggle • ctrl+a select all • ctrl+d clear • / filter • Esc focus/blur • q close") + "\n")
		if mode == "export" {
			b.WriteString(m.theme.apply(m.theme.Dim, "Action: press e to export (you will be prompted for a path)") + "\n")
		} else if mode == "import" {
			b.WriteString(m.theme.apply(m.theme.Dim, "Action: press i to import (merge/update by name; you will be prompted for a path)") + "\n")
		}
		return b.String()
	}

	// Add SSH Host modal (primary ~/.ssh/config)
	if m.showAddSSHHost {
		b.WriteString(m.theme.HeaderLine("Add SSH Host (primary ~/.ssh/config)") + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 60)))) + "\n")
		b.WriteString("\n")

		rows := []string{
			m.addSSHAlias.View(),
			m.addSSHHostName.View(),
			m.addSSHUser.View(),
			m.addSSHPort.View(),
			m.addSSHProxyJump.View(),
			fmt.Sprintf("ForwardAgent: %s   (Space toggles)", map[bool]string{true: "yes", false: "no"}[m.addSSHForwardAgent]),
			m.addSSHIdentityFile.View(),
		}

		for i, r := range rows {
			prefix := "   "
			if i == m.addSSHFieldSel {
				prefix = " > "
			}
			b.WriteString(prefix + r + "\n")
		}

		b.WriteString("\nNotes:\n")
		b.WriteString("  - HostName will be written (defaults to Alias) so it is visible/searchable like Host.\n")
		b.WriteString("  - ForwardAgent defaults to yes.\n")
		b.WriteString("  - A ~/.ssh/config.bak backup will be written.\n")
		numHint := ""
		if strings.TrimSpace(m.numBuf) != "" {
			numHint = fmt.Sprintf(" • Num: %s", m.numBuf)
		}
		b.WriteString("\nKeys: j/k move • gg/G top/bot • 1-7 then Enter jump • Enter/Tab next • Shift+Tab prev • Space toggle (ForwardAgent) • Esc cmd-mode • q cancel • Ctrl+E edit ~/.ssh/config" + numHint + "\n")
		return b.String()
	}

	// Merge duplicates confirmation modal (primary ~/.ssh/config)
	if m.showMergeDupsConfirm {
		title := "Merge duplicates"
		if strings.TrimSpace(m.mergeDupsAlias) != "" {
			title = fmt.Sprintf("Merge duplicates: %s", m.mergeDupsAlias)
		}

		b.WriteString(m.theme.HeaderLine(title) + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 50)))) + "\n")
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("Found %d duplicate Host blocks for alias: %s\n", m.mergeDupsCount, m.mergeDupsAlias))
		b.WriteString("Action:\n")
		b.WriteString("  - Replace the FIRST occurrence block with a merged block\n")
		b.WriteString("  - Delete the remaining duplicate blocks\n")
		b.WriteString("\n")
		b.WriteString("Merge semantics:\n")
		b.WriteString("  - Last-wins per key (User/Port/HostName/ProxyJump/etc)\n")
		b.WriteString("  - IdentityFile accumulates (all values kept)\n")
		if strings.TrimSpace(m.mergeDupsConfirmHint) != "" {
			b.WriteString("\n")
			b.WriteString(m.mergeDupsConfirmHint + "\n")
		}
		b.WriteString("\nKeys: y merge • n/esc cancel\n")
		return b.String()
	}

	// Dashboards browser view
	if m.showDashBrowser {
		b.WriteString(m.theme.HeaderLine("Dashboards") + "\n")
		b.WriteString(m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(m.width, 40)))) + "\n")

		// Merge YAML dashboards with recorded dashboards from state (recorded shown alongside YAML).
		allDash := make([]Dashboard, 0, 8)
		if m.cfg != nil && len(m.cfg.Dashboards) > 0 {
			allDash = append(allDash, m.cfg.Dashboards...)
		}
		if m.state != nil && len(m.state.RecordedDashboards) > 0 {
			for _, rd := range m.state.RecordedDashboards {
				allDash = append(allDash, rd.ToConfigDashboard())
			}
		}

		// Clamp selection to bounds (config may be missing; list can shrink).
		if len(allDash) == 0 {
			m.dashSelected = 0
		} else if m.dashSelected < 0 {
			m.dashSelected = 0
		} else if m.dashSelected >= len(allDash) {
			m.dashSelected = len(allDash) - 1
		}

		if len(allDash) == 0 {
			b.WriteString("No dashboards defined.\n")
		} else {
			for i, d := range allDash {
				prefix := "   "
				if i == m.dashSelected {
					prefix = " > "
				}
				line := fmt.Sprintf("%s%2d) %s", prefix, i+1, d.Name)
				if strings.TrimSpace(d.Description) != "" {
					line += " - " + d.Description
				}
				b.WriteString(line + "\n")
			}
		}
		layoutLabel := "default"
		switch m.dashLayoutMode {
		case 1:
			layoutLabel = "tiled"
		case 2:
			layoutLabel = "even-horizontal"
		case 3:
			layoutLabel = "even-vertical"
		case 4:
			layoutLabel = "main-vertical"
		case 5:
			layoutLabel = "main-horizontal"
		}
		b.WriteString(fmt.Sprintf("\nKeys: j/k move • Enter open • l layout(%s) • Esc/q close • :help help\n", layoutLabel))
		return b.String()
	}
	// Help overlay
	if m.showHelp {
		b.WriteString(m.theme.HelpText("Help") + "\n")
		b.WriteString("Motions:\n")
		b.WriteString("  j/k down/up • gg top • G bottom • u/d half-page • H/L prev/next group\n")
		b.WriteString("Search/Exit:\n")
		b.WriteString("  / search (forward) • ? search (backward) • n/N next/prev match • : command (try :menu) • q/esc quit\n\n")
		b.WriteString(m.theme.HelpText("Actions (current/selected):") + "\n")
		b.WriteString("  Ctrl+s: save current window as a recorded dashboard (auto-named)\n")
		b.WriteString("  Enter or c: connect in new tmux window (default)\n")
		b.WriteString("  v: split vertically (side-by-side) and connect\n")
		b.WriteString("  s: split horizontally (stacked) and connect\n")
		b.WriteString("  w: new window and connect   W: new window for all selected\n")
		b.WriteString("  y: yank ssh command         Y: yank commands for all selected\n")
		b.WriteString("  B: dashboards (open browser)\n")
		b.WriteString("  S: host settings (login/cred/logging/key install)\n")
		b.WriteString("  N: network view (topology) for selected/current\n")
		b.WriteString("     - Cisco IOS/XE (auto): LLDP detail (MgmtIP + System Capabilities) -> LLDP summary -> CDP\n")
		b.WriteString("     - Dell SONiC: uses 'sonic-cli -b -c \"show lldp neighbor\"'\n")
		b.WriteString("     - Per-host override: Host Settings -> 'Set Neighbor Discovery (auto/lldp/cdp)'\n")
		b.WriteString("         * auto: OS-specific fallback chain\n")
		b.WriteString("         * lldp: LLDP only (no CDP fallback)\n")
		b.WriteString("         * cdp : CDP only (no LLDP fallback; IOS/XE only)\n")
		b.WriteString("     - Identity matching: exact hostname, then shortname, then Mgmt IP / Router-ID (from Host Settings)\n")
		b.WriteString("  Space: toggle multi-select • f: toggle favorite on current\n")
		b.WriteString("  F: favorites filter • R: recents filter • A: clear filter\n\n")
		b.WriteString(m.theme.HelpText("Logs:") + "\n")
		b.WriteString("  l: open logs • O: open in $PAGER (less +F) • T: toggle logging\n")
		b.WriteString("  In logs view: j/k scroll • d/u half-page • gg/G top/bot • J/K file • r reload • q/esc close\n\n")
		b.WriteString("Press Esc or q to close help (use :help or h to open)\n")
		return b.String()
	}

	// ":" command line (vim-like)
	if m.showCmdline {
		b.WriteString(fmt.Sprintf("Command: %s\n\n", m.cmdline.View()))
	} else {
		// Search + mode line (compact "chips")
		searchLine := fmt.Sprintf("Search: %s", m.input.View())
		if strings.TrimSpace(m.input.Value()) != "" && !m.searchForward {
			searchLine += "   (reverse)"
		}

		modeBits := []string{}
		if m.filterFavorites {
			modeBits = append(modeBits, "favorites")
		}
		if m.filterRecents {
			modeBits = append(modeBits, "recents")
		}
		if len(modeBits) > 0 {
			searchLine += "   Mode: " + strings.Join(modeBits, ", ")
		}
		if m.numBuf != "" {
			searchLine += fmt.Sprintf("   Num: %s", m.numBuf)
		}
		b.WriteString(searchLine + "\n\n")
	}

	// Status (ephemeral)
	if m.status != "" && time.Now().Before(m.statusUntil) {
		b.WriteString(fmt.Sprintf("%s\n\n", m.status))
	}

	// Dimensions for two-pane layout
	listHeight := m.height - 8 // header + blank + search/status + footer estimate
	if listHeight < 8 {
		listHeight = 8
	}
	leftWidth := int(float64(m.width) * 0.56)
	if leftWidth < 40 {
		leftWidth = minInt(40, m.width-20)
	}
	if leftWidth > m.width-10 {
		leftWidth = m.width - 10
	}
	rightWidth := m.width - leftWidth - 1 // 1 for separator

	// Build left list lines
	leftLines := []string{}
	if len(m.filtered) == 0 {
		leftLines = append(leftLines, "No matches.")
	} else {
		// Ensure selection visible
		if m.selected < m.scroll {
			m.scroll = m.selected
		}
		if m.selected >= m.scroll+listHeight {
			m.scroll = m.selected - listHeight + 1
		}
		if m.scroll < 0 {
			m.scroll = 0
		}
		end := m.scroll + listHeight
		if end > len(m.filtered) {
			end = len(m.filtered)
		}
		for i := m.scroll; i < end; i++ {
			_, sel := m.selectedSet[i]
			name := m.filtered[i].Resolved.Host.Name

			display := m.filtered[i].Display
			if m.dupAliasCount != nil {
				if n := m.dupAliasCount[name]; n > 1 {
					display = fmt.Sprintf("[DUP x%d] %s", n, display)
				}
			}

			line := m.theme.ListLine(i+1, i == m.selected, sel, m.isFavorite(name), display)
			leftLines = append(leftLines, line)
		}
		if end < len(m.filtered) {
			leftLines = append(leftLines, fmt.Sprintf("… (+%d more)", len(m.filtered)-end))
		}
	}

	// Build right preview for current selection (denser)
	rightLines := []string{}
	if sel := m.current(); sel != nil {
		r := sel.Resolved
		rightLines = append(rightLines, m.theme.HeaderLine("Preview"))
		rightLines = append(rightLines, m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(rightWidth, 20)))))

		// Host summary line: user@host:port (jump ...)
		hostPart := r.Host.Name
		if r.EffectiveUser != "" {
			hostPart = r.EffectiveUser + "@" + hostPart
		}
		if r.EffectivePort > 0 {
			hostPart = fmt.Sprintf("%s:%d", hostPart, r.EffectivePort)
		}
		if r.EffectiveJumpHost != "" {
			hostPart = fmt.Sprintf("%s  (jump %s)", hostPart, r.EffectiveJumpHost)
		}
		rightLines = append(rightLines, hostPart)

		// Group (prefer configured; otherwise derive from domain)
		group := "(none)"
		if r.Group != nil && r.Group.Name != "" {
			group = r.Group.Name
		} else if idx := strings.IndexByte(r.Host.Name, '.'); idx > 0 {
			group = r.Host.Name[idx+1:]
		}

		// Group + tags on one line when possible
		metaBits := []string{"group: " + group}
		if len(r.Host.Tags) > 0 {
			metaBits = append(metaBits, "tags: "+strings.Join(r.Host.Tags, ", "))
		}
		rightLines = append(rightLines, strings.Join(metaBits, " • "))

		// Logging indicators:
		// 1) Policy (per-host extras): default ON unless explicitly disabled in ~/.config/tmux-ssh-manager/hosts/<host>.conf
		// 2) Current pane state (tmux pipe-pane): best-effort for *this* pane only.
		policy := "on"
		if ex, err := LoadHostExtras(r.Host.Name); err == nil {
			if ex.Logging {
				policy = "on"
			} else {
				policy = "off"
			}
		}

		logState := "unknown"
		if out, err := TmuxOutput("display-message", "-p", "#{pane_pipe}"); err == nil {
			v := strings.TrimSpace(out)
			if v == "1" {
				logState = "on"
			} else if v == "0" {
				logState = "off"
			}
		}
		rightLines = append(rightLines, fmt.Sprintf("logging: policy=%s • pane=%s", policy, logState))

		rightLines = append(rightLines, "")
		rightLines = append(rightLines, "SSH:")
		// Prefer wrapper form so preview matches what will actually be executed (and avoids alias recursion).
		//
		// UX: abbreviate long absolute paths to avoid wrapping the left list:
		// - if TMUX_SSH_MANAGER_BIN is an absolute path, render as "../<basename>"
		// - otherwise keep as-is
		binExec := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
		if binExec == "" {
			binExec = "tmux-ssh-manager"
		}
		binDisp := binExec
		if strings.HasPrefix(binDisp, "/") {
			binDisp = "../" + filepath.Base(binDisp)
		}

		hostKey := strings.TrimSpace(r.Host.Name)
		userArg := strings.TrimSpace(r.EffectiveUser)
		userFlag := ""
		if userArg != "" {
			userFlag = " --user " + shellEscapeForSh(userArg)
		}

		// Render the wrapper binary on its own line to avoid overflow/wrapping into the left list.
		rightLines = append(rightLines, "  "+shellEscapeForSh(binDisp))
		rightLines = append(rightLines, "  ssh --tmux --host "+shellEscapeForSh(hostKey)+userFlag)

		// Duplicate warning (primary ~/.ssh/config only)
		if m.dupAliasCount != nil {
			if n := m.dupAliasCount[hostKey]; n > 1 {
				rightLines = append(rightLines, "")
				rightLines = append(rightLines, fmt.Sprintf("WARNING: %d duplicate Host blocks in ~/.ssh/config", n))
				rightLines = append(rightLines, "  Press M to merge (replace first, delete rest)")
			}
		}

		// Selection state (keep brief)
		rightLines = append(rightLines, "")
		selCount := m.selectedCount()
		if selCount > 0 {
			rightLines = append(rightLines, fmt.Sprintf("selected: %d", selCount))
		}
		if m.isFavorite(r.Host.Name) {
			rightLines = append(rightLines, "★ favorite")
		}
	} else {
		rightLines = append(rightLines, m.theme.HeaderLine("Preview"))
		rightLines = append(rightLines, m.theme.apply(m.theme.Separator, strings.Repeat("-", maxInt(6, minInt(rightWidth, 20)))))
		rightLines = append(rightLines, "(no selection)")
	}

	// If log viewer is active, render it instead of the normal selector columns.
	if m.showLogs {
		return m.viewLogs()
	}

	// Render two columns (width-aware, ANSI-safe)
	//
	// Important: do NOT use len() for truncation/padding because these strings can contain
	// ANSI escape sequences and/or multibyte runes. Using byte-length causes the right
	// column to "jitter" or appear to shift as you move selection.
	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}

	leftStyle := lipgloss.NewStyle().Width(maxInt(0, leftWidth)).MaxWidth(maxInt(0, leftWidth))
	rightStyle := lipgloss.NewStyle().Width(maxInt(0, rightWidth)).MaxWidth(maxInt(0, rightWidth))

	for i := 0; i < maxLines; i++ {
		ll := ""
		rl := ""
		if i < len(leftLines) {
			ll = leftLines[i]
		}
		if i < len(rightLines) {
			rl = rightLines[i]
		}

		// Clamp + pad to fixed widths.
		if leftWidth > 0 {
			ll = leftStyle.Render(ll)
		}
		if rightWidth > 0 {
			rl = rightStyle.Render(rl)
		}

		sep := " "
		if rightWidth > 0 {
			sep = m.theme.SeparatorRune()
		}
		b.WriteString(ll + sep + rl + "\n")
	}

	// Footer hints (essentials only; full reference lives in :menu and :help)
	managed := 0
	if m.paneHost != nil {
		managed = len(m.paneHost)
	}
	recState := "OFF"
	if m.recording {
		recState = "ON"
	}
	b.WriteString(fmt.Sprintf("\nKeys: j/k move • / search • ? reverse • Enter connect • Space multi • e edit-ssh • S host settings • E ssh-export • I ssh-import • M merge-dups • :menu • :help • q quit   |   managed panes: %d   |   REC: %s\n", managed, recState))

	return b.String()
}

// --- Helpers ---

func (m *model) recomputeFilter() {
	q := strings.TrimSpace(m.input.Value())
	all := rankMatches(m.candidates, q)

	// apply favorites/recents filters if set
	filtered := make([]candidate, 0, len(all))
	if m.filterFavorites || m.filterRecents {
		rec := make(map[string]struct{}, len(m.recents))
		for _, n := range m.recents {
			rec[n] = struct{}{}
		}
		for _, c := range all {
			name := c.Resolved.Host.Name
			if m.filterFavorites && !m.isFavorite(name) {
				continue
			}
			if m.filterRecents {
				if _, ok := rec[name]; !ok {
					continue
				}
			}
			filtered = append(filtered, c)
		}
	} else {
		filtered = all
	}

	m.filtered = filtered
	if m.selected >= len(m.filtered) {
		m.selected = len(m.filtered) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
	m.scroll = 0
}

// favorites helpers
func (m *model) isFavorite(name string) bool {
	_, ok := m.favorites[name]
	return ok
}
func (m *model) favorite(name string) {
	m.favorites[name] = struct{}{}
}
func (m *model) unfavorite(name string) {
	delete(m.favorites, name)
}

// recents helpers
func (m *model) addRecent(name string) {
	// move to front; keep unique; cap at 50
	if name == "" {
		return
	}
	// remove if exists
	newList := make([]string, 0, len(m.recents)+1)
	newList = append(newList, name)
	for _, n := range m.recents {
		if n != name {
			newList = append(newList, n)
		}
	}
	if len(newList) > 50 {
		newList = newList[:50]
	}
	m.recents = newList
}

// selection helpers
func (m *model) selectedCount() int {
	return len(m.selectedSet)
}
func (m *model) selectedResolved() []ResolvedHost {
	if len(m.selectedSet) == 0 {
		return nil
	}
	out := make([]ResolvedHost, 0, len(m.selectedSet))
	for idx := range m.selectedSet {
		if idx >= 0 && idx < len(m.filtered) {
			out = append(out, m.filtered[idx].Resolved)
		}
	}
	return out
}

// group navigation
func groupKeyForCandidate(c candidate) string {
	if c.Resolved.Group != nil && c.Resolved.Group.Name != "" {
		return c.Resolved.Group.Name
	}
	name := c.Resolved.Host.Name
	if i := strings.IndexByte(name, '.'); i > 0 && i+1 < len(name) {
		return name[i+1:]
	}
	return "(ungrouped)"
}
func (m *model) jumpGroupPrev() {
	if len(m.filtered) == 0 {
		return
	}
	curKey := groupKeyForCandidate(m.filtered[m.selected])
	// scan backwards for first index with a different group, then jump to its first occurrence
	targetKey := curKey
	for i := m.selected - 1; i >= 0; i-- {
		g := groupKeyForCandidate(m.filtered[i])
		if g != targetKey {
			// find first index of this group
			first := i
			for j := i - 1; j >= 0; j-- {
				if groupKeyForCandidate(m.filtered[j]) != g {
					break
				}
				first = j
			}
			m.selected = first
			return
		}
	}
	// wrap to first if none
	m.selected = 0
}
func (m *model) jumpGroupNext() {
	if len(m.filtered) == 0 {
		return
	}
	curKey := groupKeyForCandidate(m.filtered[m.selected])
	// scan forward for first index with a different group
	for i := m.selected + 1; i < len(m.filtered); i++ {
		g := groupKeyForCandidate(m.filtered[i])
		if g != curKey {
			m.selected = i
			return
		}
	}
	// wrap to last
	m.selected = len(m.filtered) - 1
}

// util
// padRight previously did byte-length based padding/truncation, which breaks alignment for
// ANSI-styled strings and multibyte runes. Column layout is now handled by lipgloss width-aware
// rendering; keep this function as a no-op wrapper for backward compatibility (not used).
func padRight(s string, w int) string {
	return s
}

func (m *model) saveState() {
	if m == nil {
		return
	}
	// Compose state from current favorites/recents (+ recorded dashboards if present)
	st := &State{
		Version:   1,
		Favorites: make([]string, 0, len(m.favorites)),
		Recents:   append([]string(nil), m.recents...),
	}
	for n := range m.favorites {
		if strings.TrimSpace(n) != "" {
			st.Favorites = append(st.Favorites, n)
		}
	}

	// Preserve/emit recorded dashboards:
	// - m.state is loaded at startup and may already contain recordings
	// - we keep them stable unless a future UI flow updates them
	if m.state != nil && len(m.state.RecordedDashboards) > 0 {
		st.RecordedDashboards = append([]RecordedDashboard(nil), m.state.RecordedDashboards...)
	}

	path := m.statePath
	if path == "" {
		if p, err := DefaultStatePath(); err == nil {
			path = p
		}
	}
	_ = SaveState(path, st)
	m.state = st
}

func (m *model) current() *candidate {
	if len(m.filtered) == 0 || m.selected < 0 || m.selected >= len(m.filtered) {
		return nil
	}
	return &m.filtered[m.selected]
}

func (m *model) move(delta int) {
	if len(m.filtered) == 0 {
		return
	}
	m.pendingG = false
	m.numBuf = ""
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(m.filtered) {
		m.selected = len(m.filtered) - 1
	}
}

func (m *model) gotoTop() {
	m.selected = 0
	m.scroll = 0
}

func (m *model) gotoBottom() {
	if len(m.filtered) == 0 {
		return
	}
	m.selected = len(m.filtered) - 1
}

func (m *model) pageUp() {
	if len(m.filtered) == 0 {
		return
	}
	half := maxInt(3, m.opts.MaxResults/2)
	m.selected -= half
	if m.selected < 0 {
		m.selected = 0
	}
}

func (m *model) pageDown() {
	if len(m.filtered) == 0 {
		return
	}
	half := maxInt(3, m.opts.MaxResults/2)
	m.selected += half
	if m.selected >= len(m.filtered) {
		m.selected = len(m.filtered) - 1
	}
}

func (m *model) setStatus(s string, ms int) {
	m.status = s
	m.statusUntil = time.Now().Add(time.Duration(ms) * time.Millisecond)
}

// --- Network view helpers (collection + rendering + navigation) ---

func (m *model) netFocusedNode() *netNode {
	if strings.TrimSpace(m.netSelectedID) == "" {
		return nil
	}
	if m.netNodeIndex == nil {
		return nil
	}
	if i, ok := m.netNodeIndex[m.netSelectedID]; ok && i >= 0 && i < len(m.netNodes) {
		return &m.netNodes[i]
	}
	return nil
}

func (m *model) netFocusNext() {
	if len(m.netNodes) == 0 {
		return
	}
	if m.netNodeIndex == nil {
		m.netNodeIndex = make(map[string]int)
		for i := range m.netNodes {
			m.netNodeIndex[m.netNodes[i].ID] = i
		}
	}
	cur := 0
	if strings.TrimSpace(m.netSelectedID) != "" {
		if i, ok := m.netNodeIndex[m.netSelectedID]; ok {
			cur = i
		}
	}
	nxt := (cur + 1) % len(m.netNodes)
	m.netSelectedID = m.netNodes[nxt].ID
}

func (m *model) netFocusPrev() {
	if len(m.netNodes) == 0 {
		return
	}
	if m.netNodeIndex == nil {
		m.netNodeIndex = make(map[string]int)
		for i := range m.netNodes {
			m.netNodeIndex[m.netNodes[i].ID] = i
		}
	}
	cur := 0
	if strings.TrimSpace(m.netSelectedID) != "" {
		if i, ok := m.netNodeIndex[m.netSelectedID]; ok {
			cur = i
		}
	}
	prv := cur - 1
	if prv < 0 {
		prv = len(m.netNodes) - 1
	}
	m.netSelectedID = m.netNodes[prv].ID
}

func (m *model) netFocusByQuery(q string) {
	q = strings.TrimSpace(q)
	if q == "" || len(m.netNodes) == 0 {
		return
	}
	qn := strings.ToLower(q)

	// First pass: match by name substring (preferred).
	for i := range m.netNodes {
		n := &m.netNodes[i]
		if strings.Contains(strings.ToLower(n.DisplayName), qn) {
			m.netSelectedID = n.ID
			return
		}
	}

	// Second pass: match by identity hint (IP-like).
	for i := range m.netNodes {
		n := &m.netNodes[i]
		if strings.Contains(strings.ToLower(n.IdentityHint), qn) {
			m.netSelectedID = n.ID
			return
		}
	}
}

func renderNetASCII(width, height int, nodes []netNode, edges []netEdge, focusedID string) string {
	_ = height

	// Compact list-style topology (legacy):
	// - configured nodes are "first class" (uppercase marker)
	// - unknown nodes and configured islands are downregulated (lowercase marker)
	// - show neighbor count and a few link hints to avoid overwhelming
	if width <= 0 {
		width = 80
	}

	adj := make(map[string][]netEdge)
	for _, e := range edges {
		adj[e.FromID] = append(adj[e.FromID], e)
	}

	var b strings.Builder
	b.WriteString("Nodes:\n")

	maxLinksPreview := 3
	for _, n := range nodes {
		prefix := "  "
		if n.ID == focusedID {
			prefix = "> "
		}
		kind := "[cfg]"
		if !n.Configured {
			kind = "[unk]"
		}
		marker := "●"
		if n.Downregulated {
			marker = "·"
		}
		name := n.DisplayName
		if strings.TrimSpace(name) == "" {
			name = n.ID
		}

		links := adj[n.ID]
		line := fmt.Sprintf("%s%s %s %s", prefix, marker, kind, name)
		if n.IdentityHint != "" {
			line += " (" + n.IdentityHint + ")"
		}
		if len(links) > 0 {
			line += fmt.Sprintf("  — %d link(s)", len(links))
		} else {
			line += "  — 0 link(s)"
		}
		if len(line) > width {
			line = line[:maxInt(0, width-1)] + "…"
		}
		b.WriteString(line + "\n")

		// Preview a few outgoing edges.
		if len(links) > 0 {
			show := links
			if len(show) > maxLinksPreview {
				show = show[:maxLinksPreview]
			}
			for _, e := range show {
				to := e.ToID
				for _, nn := range nodes {
					if nn.ID == to {
						to = nn.DisplayName
						break
					}
				}
				hint := fmt.Sprintf("     - %s -> %s", strings.TrimSpace(e.LocalPort), strings.TrimSpace(to))
				if strings.TrimSpace(e.RemotePort) != "" {
					hint += fmt.Sprintf(" (%s)", strings.TrimSpace(e.RemotePort))
				}
				if len(hint) > width {
					hint = hint[:maxInt(0, width-1)] + "…"
				}
				b.WriteString(hint + "\n")
			}
			if len(links) > maxLinksPreview {
				b.WriteString(fmt.Sprintf("     - +%d more…\n", len(links)-maxLinksPreview))
			}
		}
	}

	b.WriteString("\nLegend: ● primary  · downregulated (unknown or island)\n")
	return b.String()
}

// renderNetEdgesView renders a sortable, copy/paste-friendly edge list.
// This is the “authoritative truth” view for LLDP adjacency.
func renderNetEdgesView(width, height int, nodes []netNode, edges []netEdge, focusedID string, box bool) string {
	_ = height
	if width <= 0 {
		width = 80
	}

	// Map id -> display name
	nameOf := func(id string) string {
		for _, n := range nodes {
			if n.ID == id {
				if strings.TrimSpace(n.DisplayName) != "" {
					return n.DisplayName
				}
				return n.ID
			}
		}
		return id
	}

	type row struct {
		a, ap string
		b, bp string
	}
	rows := make([]row, 0, len(edges))
	seen := make(map[string]struct{})

	for _, e := range edges {
		// Undirected-ish de-dupe key (order-insensitive) based on endpoints + ports.
		a := e.FromID
		bb := e.ToID
		ap := strings.TrimSpace(e.LocalPort)
		bp := strings.TrimSpace(e.RemotePort)

		k1 := a + "|" + ap + "<->" + bb + "|" + bp
		k2 := bb + "|" + bp + "<->" + a + "|" + ap
		if _, ok := seen[k1]; ok {
			continue
		}
		if _, ok := seen[k2]; ok {
			continue
		}
		seen[k1] = struct{}{}
		rows = append(rows, row{a: a, ap: ap, b: bb, bp: bp})
	}

	sort.Slice(rows, func(i, j int) bool {
		ai := strings.ToLower(nameOf(rows[i].a))
		aj := strings.ToLower(nameOf(rows[j].a))
		if ai == aj {
			bi := strings.ToLower(nameOf(rows[i].b))
			bj := strings.ToLower(nameOf(rows[j].b))
			if bi == bj {
				return rows[i].ap < rows[j].ap
			}
			return bi < bj
		}
		return ai < aj
	})

	link := "<->"
	if box {
		link = "↔"
	}

	var b strings.Builder
	b.WriteString("Edges:\n\n")
	for _, r := range rows {
		left := nameOf(r.a)
		right := nameOf(r.b)
		line := fmt.Sprintf("  %s:%s %s %s:%s", left, r.ap, link, right, r.bp)
		if strings.TrimSpace(focusedID) != "" && (r.a == focusedID || r.b == focusedID) {
			line = "> " + strings.TrimPrefix(line, "  ")
		}
		if len(line) > width {
			line = line[:maxInt(0, width-1)] + "…"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nTip: press m to cycle views. Focused node highlights related edges.\n")
	return b.String()
}

// renderNetLayered renders a simple layered “fabric-like” view.
// It is intentionally heuristic and designed for leaf/spine-ish topologies.
func renderNetLayered(width, height int, nodes []netNode, edges []netEdge, focusedID string, box bool) string {
	_ = height
	if width <= 0 {
		width = 80
	}

	// Basic degree calculation (undirected)
	deg := make(map[string]int)
	for _, e := range edges {
		deg[e.FromID]++
		deg[e.ToID]++
	}

	// Choose "spines" as the top N highest-degree nodes (configured preferred).
	type cand struct {
		id         string
		deg        int
		configured bool
		name       string
	}
	cands := make([]cand, 0, len(nodes))
	for _, n := range nodes {
		cands = append(cands, cand{
			id:         n.ID,
			deg:        deg[n.ID],
			configured: n.Configured,
			name:       strings.ToLower(strings.TrimSpace(n.DisplayName)),
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].deg != cands[j].deg {
			return cands[i].deg > cands[j].deg
		}
		if cands[i].configured != cands[j].configured {
			return cands[i].configured
		}
		return cands[i].name < cands[j].name
	})

	// Pick up to 2 spines by default; scale a bit with node count.
	maxSpines := 2
	if len(nodes) >= 6 {
		maxSpines = 3
	}
	if len(nodes) >= 10 {
		maxSpines = 4
	}
	if maxSpines > len(cands) {
		maxSpines = len(cands)
	}
	spines := make([]string, 0, maxSpines)
	for i := 0; i < maxSpines; i++ {
		if cands[i].deg > 0 {
			spines = append(spines, cands[i].id)
		}
	}
	if len(spines) == 0 && len(nodes) > 0 {
		spines = append(spines, nodes[0].ID)
	}

	// Build adjacency (undirected view for layering)
	adj := make(map[string][]string)
	portHint := make(map[string][]string) // key "a|b" => []"aPort↔bPort"
	for _, e := range edges {
		adj[e.FromID] = append(adj[e.FromID], e.ToID)
		adj[e.ToID] = append(adj[e.ToID], e.FromID)
		k := e.FromID + "|" + e.ToID
		h := strings.TrimSpace(e.LocalPort)
		if strings.TrimSpace(e.RemotePort) != "" {
			h += ":" + strings.TrimSpace(e.RemotePort)
		}
		if h != "" {
			portHint[k] = append(portHint[k], h)
		}
	}

	// BFS layering from spines.
	layer := make(map[string]int)
	const inf = 1 << 30
	for _, n := range nodes {
		layer[n.ID] = inf
	}
	queue := make([]string, 0, len(nodes))
	for _, s := range spines {
		layer[s] = 0
		queue = append(queue, s)
	}
	for qi := 0; qi < len(queue); qi++ {
		u := queue[qi]
		for _, v := range adj[u] {
			if layer[v] > layer[u]+1 {
				layer[v] = layer[u] + 1
				queue = append(queue, v)
			}
		}
	}

	// Group nodes by layer (cap layers to keep output bounded)
	maxLayer := 0
	for _, n := range nodes {
		if layer[n.ID] != inf && layer[n.ID] > maxLayer {
			maxLayer = layer[n.ID]
		}
	}
	if maxLayer > 4 {
		maxLayer = 4
	}

	byLayer := make([][]string, maxLayer+2) // +1 for inf/unreached
	for _, n := range nodes {
		l := layer[n.ID]
		if l == inf || l > maxLayer {
			l = maxLayer + 1
		}
		byLayer[l] = append(byLayer[l], n.ID)
	}

	// id -> name
	nameOf := func(id string) string {
		for _, n := range nodes {
			if n.ID == id {
				if strings.TrimSpace(n.DisplayName) != "" {
					return n.DisplayName
				}
				return n.ID
			}
		}
		return id
	}

	for i := range byLayer {
		sort.Slice(byLayer[i], func(a, b int) bool {
			return strings.ToLower(nameOf(byLayer[i][a])) < strings.ToLower(nameOf(byLayer[i][b]))
		})
	}

	// Drawing glyphs
	hLine := "-"
	vLine := "|"
	junc := "+"
	arrow := "->"
	if box {
		hLine = "─"
		vLine = "│"
		junc = "┼"
		arrow = "→"
	}

	var b strings.Builder
	b.WriteString("Layered view (heuristic):\n\n")

	labelNode := func(id string) string {
		nm := nameOf(id)
		prefix := "  "
		if id == focusedID {
			prefix = "> "
		}
		return prefix + "[" + nm + "]"
	}

	for l := 0; l < len(byLayer); l++ {
		if len(byLayer[l]) == 0 {
			continue
		}
		title := "Layer " + strconv.Itoa(l)
		if l == 0 {
			title = "Spines (layer 0)"
		}
		if l == maxLayer+1 {
			title = "Unreached/other"
		}
		b.WriteString(title + ":\n")
		for _, id := range byLayer[l] {
			line := labelNode(id)
			if len(line) > width {
				line = line[:maxInt(0, width-1)] + "…"
			}
			b.WriteString(line + "\n")

			// Show neighbors on next layer (or same layer if no layer info) with port hints.
			neis := adj[id]
			if len(neis) == 0 {
				continue
			}
			// Prefer neighbors “downstream” (layer+1)
			down := make([]string, 0, len(neis))
			same := make([]string, 0, len(neis))
			for _, v := range neis {
				if layer[v] == layer[id]+1 {
					down = append(down, v)
				} else if layer[v] == layer[id] {
					same = append(same, v)
				}
			}
			targets := down
			if len(targets) == 0 {
				targets = same
			}
			sort.Slice(targets, func(i, j int) bool { return strings.ToLower(nameOf(targets[i])) < strings.ToLower(nameOf(targets[j])) })
			maxShow := 4
			if len(targets) > maxShow {
				targets = targets[:maxShow]
			}
			for _, v := range targets {
				ph := ""
				// show first port hint if present
				k := id + "|" + v
				if hs := portHint[k]; len(hs) > 0 {
					ph = hs[0]
				}
				conn := "   " + vLine + " " + arrow + " [" + nameOf(v) + "]"
				if ph != "" {
					conn += " (" + ph + ")"
				}
				// add a little horizontal separator so it reads like a diagram, not a list
				conn = strings.Replace(conn, arrow, junc+strings.Repeat(hLine, 2)+arrow+strings.Repeat(hLine, 1), 1)
				if len(conn) > width {
					conn = conn[:maxInt(0, width-1)] + "…"
				}
				b.WriteString(conn + "\n")
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("Legend: [name] node  " + junc + hLine + hLine + arrow + " link (ports in parens)\n")
	b.WriteString("Tip: press m to cycle views, b to toggle box drawing.\n")
	return b.String()
}

func buildNetGraph(cfg *Config, targets []ResolvedHost, results []LLDPParseResult, failures map[string]string) ([]netNode, []netEdge, map[string]int) {
	if cfg == nil {
		return nil, nil, map[string]int{}
	}
	if failures == nil {
		failures = make(map[string]string)
	}

	g, err := BuildTopologyGraph(cfg, targets, results, DefaultBuildTopologyOptions())
	if err != nil {
		// If graph build fails, still show selected targets as downregulated islands.
		nodes := make([]netNode, 0, len(targets))
		for i := range targets {
			r := targets[i]
			nodes = append(nodes, netNode{
				ID:            "cfg:" + NormalizeHostFull(r.Host.Name),
				DisplayName:   r.Host.Name,
				Configured:    true,
				Downregulated: true,
				Resolved:      &r,
			})
		}
		idx := make(map[string]int, len(nodes))
		for i := range nodes {
			idx[nodes[i].ID] = i
		}
		return nodes, nil, idx
	}

	// Build node list
	nodes := make([]netNode, 0, len(g.Nodes))
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		nn := netNode{
			ID:           id,
			DisplayName:  strings.TrimSpace(n.Label),
			Configured:   n.Kind == NodeConfigured,
			Resolved:     nil,
			IdentityHint: "",
		}
		if n.Kind == NodeConfigured && n.KnownResolved != nil {
			r := *n.KnownResolved
			nn.Resolved = &r
			// Prefer router-id then mgmt ip as identity hint.
			ips := ChooseIdentityIPs(n.Extras.RouterID, []string{n.Extras.MgmtIP})
			if len(ips) > 0 {
				nn.IdentityHint = ips[0]
			}
			// Downregulate configured islands or no-data nodes
			if n.Island {
				nn.Downregulated = true
			}
		} else {
			nn.Downregulated = true
			// Use any discovered mgmt ip as identity hint if present.
			if len(n.IdentityIPs) > 0 {
				nn.IdentityHint = n.IdentityIPs[0]
			}
		}

		// If this configured target had explicit failure, force downregulation but keep configured.
		if nn.Configured {
			nameKey := ""
			if nn.Resolved != nil {
				nameKey = strings.TrimSpace(nn.Resolved.Host.Name)
			} else if strings.HasPrefix(nn.ID, "cfg:") {
				nameKey = strings.TrimPrefix(nn.ID, "cfg:")
			}
			if nameKey != "" {
				if _, ok := failures[nameKey]; ok {
					nn.Downregulated = true
				}
			}
		}

		if nn.DisplayName == "" {
			nn.DisplayName = nn.ID
		}
		nodes = append(nodes, nn)
	}

	// Stable sort: configured first, then unknown; within group by name
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Configured != nodes[j].Configured {
			return nodes[i].Configured
		}
		return strings.ToLower(nodes[i].DisplayName) < strings.ToLower(nodes[j].DisplayName)
	})

	nodeIndex := make(map[string]int, len(nodes))
	for i := range nodes {
		nodeIndex[nodes[i].ID] = i
	}

	// Build edges list (directional)
	edges := make([]netEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, netEdge{
			FromID:     e.FromNodeID,
			ToID:       e.ToNodeID,
			LocalPort:  strings.TrimSpace(e.LocalPort),
			RemotePort: strings.TrimSpace(e.RemotePort),
		})
	}

	return nodes, edges, nodeIndex
}

func runLLDPCollectionCmd(cfg *Config, targets []ResolvedHost) tea.Cmd {
	// Run SSH LLDP collection concurrently (bounded) and return a done message.
	return func() tea.Msg {
		if cfg == nil {
			return netLLDPDoneMsg{Err: "network: nil config", Status: "failed", Targets: targets, Results: nil, Failures: map[string]string{}}
		}
		if len(targets) == 0 {
			return netLLDPDoneMsg{Err: "network: no targets", Status: "no targets", Targets: targets, Results: nil, Failures: map[string]string{}}
		}

		failures := make(map[string]string)
		results := make([]LLDPParseResult, 0, len(targets))

		// Simple bounded concurrency.
		type item struct {
			r ResolvedHost
		}
		in := make(chan item, len(targets))
		out := make(chan struct {
			host string
			res  *LLDPParseResult
			err  error
			raw  string
		}, len(targets))

		workers := 6
		if len(targets) < workers {
			workers = len(targets)
		}
		if workers <= 0 {
			workers = 1
		}

		for w := 0; w < workers; w++ {
			go func() {
				for it := range in {
					hostKey := strings.TrimSpace(it.r.Host.Name)
					res, raw, err := collectLLDPForHost(cfg, it.r)
					out <- struct {
						host string
						res  *LLDPParseResult
						err  error
						raw  string
					}{host: hostKey, res: res, err: err, raw: raw}
				}
			}()
		}

		for _, r := range targets {
			in <- item{r: r}
		}
		close(in)

		for i := 0; i < len(targets); i++ {
			got := <-out
			if got.err != nil {
				failures[got.host] = got.err.Error()
				continue
			}
			if got.res != nil {
				results = append(results, *got.res)
			}
		}

		status := fmt.Sprintf("collected %d/%d", len(results), len(targets))
		errStr := ""
		if len(failures) > 0 {
			errStr = fmt.Sprintf("%d device(s) had errors", len(failures))
		}

		return netLLDPDoneMsg{
			Status:   status,
			Err:      errStr,
			Targets:  targets,
			Results:  results,
			Failures: failures,
		}
	}
}

func collectLLDPForHost(cfg *Config, r ResolvedHost) (*LLDPParseResult, string, error) {
	// Determine network OS selection.
	// Prefer per-host extras device_os if set; otherwise fall back to YAML network_os.
	osID := ""
	if ex, err := LoadHostExtras(strings.TrimSpace(r.Host.Name)); err == nil {
		if v := strings.TrimSpace(strings.ToLower(ex.DeviceOS)); v != "" {
			osID = v
		}
	}
	if osID == "" {
		osID = strings.ToLower(strings.TrimSpace(r.Host.NetworkOS))
	}
	if osID == "" {
		return &LLDPParseResult{
			LocalDevice:      r.Host.Name,
			Entries:          nil,
			IdentityHintsIPs: nil,
			ParseWarnings:    []string{"host missing device_os/network_os; treated as island"},
		}, "(no device_os/network_os configured)", nil
	}

	// Per-host discovery preference (auto/lldp/cdp) is stored in host extras:
	// neighbor_discovery=auto|lldp|cdp
	//
	// We use it to influence the *order* of protocol/command attempts (and optionally skip one),
	// while still keeping the general fallback behavior.
	pref := "auto"
	if ex, err := LoadHostExtras(strings.TrimSpace(r.Host.Name)); err == nil {
		if v := strings.TrimSpace(strings.ToLower(ex.NeighborDiscovery)); v != "" {
			pref = v
		}
	}

	specs := DefaultNeighborCommandsForHost(osID, pref)
	if len(specs) == 0 {
		return &LLDPParseResult{
			LocalDevice:      r.Host.Name,
			Entries:          nil,
			IdentityHintsIPs: nil,
			ParseWarnings:    []string{"no discovery command spec for network_os=" + osID},
		}, "(no discovery command spec for network_os=" + osID + ")", nil
	}

	var lastErr error
	lastOut := ""
	for _, spec := range specs {
		ctx, cancel := context.WithTimeout(context.Background(), spec.Timeout)
		if spec.Timeout <= 0 {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 8*time.Second)
		}
		defer cancel()

		argv := BuildSSHCommand(r,
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "ConnectTimeout=5",
			"--",
			spec.Command,
		)

		// If askpass mode is enabled for this host AND a credential exists, run discovery using SSH_ASKPASS.
		// This allows password-only devices to participate in Network View without requiring key auth.
		//
		// We mimic the scp wrapper approach:
		// - create a tiny askpass wrapper script that execs tmux-ssh-manager __askpass via an absolute path
		// - force askpass usage by setting SSH_ASKPASS_REQUIRE=force and a dummy DISPLAY
		// - redirect stdin from /dev/null so ssh can't read from a TTY and will invoke askpass
		useAskpass := false
		credHostKey := strings.TrimSpace(r.Host.Name)
		credUser := strings.TrimSpace(r.EffectiveUser)

		// Determine effective login mode using the same precedence rules as the rest of the TUI.
		// (HostExtras auth_mode=keychain => askpass; otherwise fall back to YAML login_mode.)
		effLoginMode := "manual"
		if hk := strings.TrimSpace(r.Host.Name); hk != "" {
			if ex, e := LoadHostExtras(hk); e == nil {
				switch strings.ToLower(strings.TrimSpace(ex.AuthMode)) {
				case "keychain":
					effLoginMode = "askpass"
				case "manual":
					effLoginMode = "manual"
				}
			}
		}
		if effLoginMode == "manual" {
			lm := strings.ToLower(strings.TrimSpace(r.Host.LoginMode))
			if lm != "" {
				effLoginMode = lm
			}
		}

		if strings.EqualFold(strings.TrimSpace(effLoginMode), "askpass") {
			if err := CredGet(credHostKey, credUser, "password"); err == nil {
				useAskpass = true
			}
		}

		// Optional: SSH ControlMaster multiplexing to reuse auth/transport across discovery + interactive sessions.
		// Enable with: TMUX_SSH_MANAGER_SSH_MUX=1
		//
		// NOTE: This does NOT "reuse the same pane"; it reuses the underlying SSH transport so
		// subsequent connections are fast and avoid re-prompting for passwords.
		useMux := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_SSH_MUX")) != ""
		controlPath := ""
		if useMux {
			// Put sockets under XDG-ish config dir when possible.
			baseDir := ""
			if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
				baseDir = filepath.Join(xdg, "tmux-ssh-manager", "mux")
			} else if home, _ := os.UserHomeDir(); strings.TrimSpace(home) != "" {
				baseDir = filepath.Join(home, ".config", "tmux-ssh-manager", "mux")
			} else {
				baseDir = filepath.Join(os.TempDir(), "tmux-ssh-manager-mux")
			}
			_ = os.MkdirAll(baseDir, 0o700)

			// Keep ControlPath short to avoid UNIX socket path length limits.
			// Hash the tuple (user, host, port, jump) into a stable filename.
			h := sha256.Sum256([]byte(
				strings.TrimSpace(r.EffectiveUser) + "|" +
					strings.TrimSpace(r.Host.Name) + "|" +
					strconv.Itoa(r.EffectivePort) + "|" +
					strings.TrimSpace(r.EffectiveJumpHost),
			))
			controlPath = filepath.Join(baseDir, "cm-"+hex.EncodeToString(h[:8])+".sock")

			// Warm the master connection (best-effort, non-fatal).
			//
			// ssh -M -N -f -o ControlMaster=yes -o ControlPersist=10m -S <path> DEST
			masterArgv := BuildSSHCommand(r,
				"-o", "StrictHostKeyChecking=accept-new",
				"-o", "ConnectTimeout=5",
				"-o", "ControlMaster=yes",
				"-o", "ControlPersist=10m",
				"-S", controlPath,
				"-N",
				"-f",
			)

			// For master creation, prefer the same auth mode we're about to use (askpass vs batch).
			if useAskpass {
				bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
				if bin == "" {
					if exe, e := os.Executable(); e == nil && strings.TrimSpace(exe) != "" {
						bin = strings.TrimSpace(exe)
					} else {
						bin = "tmux-ssh-manager"
					}
				}
				askpassArgs := []string{
					"__askpass",
					"--host", credHostKey,
				}
				if credUser != "" {
					askpassArgs = append(askpassArgs, "--user", credUser)
				}
				askpassArgs = append(askpassArgs, "--kind", "password")

				tmpDir := strings.TrimSpace(os.Getenv("TMPDIR"))
				if tmpDir == "" {
					tmpDir = os.TempDir()
				}
				wrapperPath := filepath.Join(tmpDir, fmt.Sprintf("tssm-askpass-%d.sh", os.Getpid()))
				wrapper := "#!/usr/bin/env bash\n" +
					"exec " + shellEscapeForSh(bin) + " " + shellQuoteCmdSimple(askpassArgs) + "\n"
				_ = os.WriteFile(wrapperPath, []byte(wrapper), 0o700)

				env := append([]string(nil), os.Environ()...)
				env = append(env, "SSH_ASKPASS="+wrapperPath)
				env = append(env, "SSH_ASKPASS_REQUIRE=force")
				env = append(env, "DISPLAY=1")

				devNull, dnErr := os.Open("/dev/null")
				if dnErr == nil {
					// best-effort; ignore errors
					cmd := exec.CommandContext(ctx, masterArgv[0], masterArgv[1:]...)
					cmd.Env = env
					cmd.Stdin = devNull
					_ = cmd.Run()
					_ = devNull.Close()
				} else {
					cmd := exec.CommandContext(ctx, masterArgv[0], masterArgv[1:]...)
					cmd.Env = env
					_ = cmd.Run()
				}
			} else {
				// Non-interactive best-effort master (BatchMode).
				masterArgv = append([]string{masterArgv[0]}, append([]string{"-o", "BatchMode=yes"}, masterArgv[1:]...)...)
				cmd := exec.CommandContext(ctx, masterArgv[0], masterArgv[1:]...)
				_ = cmd.Run()
			}
		}

		var outBytes []byte
		var err error
		outStr := ""

		if useAskpass {
			bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
			if bin == "" {
				// Prefer an absolute path to this running binary when possible.
				if exe, e := os.Executable(); e == nil && strings.TrimSpace(exe) != "" {
					bin = strings.TrimSpace(exe)
				} else {
					bin = "tmux-ssh-manager"
				}
			}

			askpassArgs := []string{
				"__askpass",
				"--host", credHostKey,
			}
			if credUser != "" {
				askpassArgs = append(askpassArgs, "--user", credUser)
			}
			askpassArgs = append(askpassArgs, "--kind", "password")

			tmpDir := strings.TrimSpace(os.Getenv("TMPDIR"))
			if tmpDir == "" {
				tmpDir = os.TempDir()
			}
			wrapperPath := filepath.Join(tmpDir, fmt.Sprintf("tssm-askpass-%d.sh", os.Getpid()))
			wrapper := "#!/usr/bin/env bash\n" +
				"exec " + shellEscapeForSh(bin) + " " + shellQuoteCmdSimple(askpassArgs) + "\n"
			_ = os.WriteFile(wrapperPath, []byte(wrapper), 0o700)

			env := append([]string(nil), os.Environ()...)
			env = append(env, "SSH_ASKPASS="+wrapperPath)
			env = append(env, "SSH_ASKPASS_REQUIRE=force")
			env = append(env, "DISPLAY=1")

			// If mux is enabled, make the discovery command use the warmed master socket.
			if useMux && strings.TrimSpace(controlPath) != "" {
				argv = BuildSSHCommand(r,
					"-o", "StrictHostKeyChecking=accept-new",
					"-o", "ConnectTimeout=5",
					"-o", "ControlMaster=auto",
					"-o", "ControlPersist=10m",
					"-S", controlPath,
					"--",
					spec.Command,
				)
			}

			devNull, dnErr := os.Open("/dev/null")
			if dnErr != nil {
				// If we can't control stdin, fall back to normal exec (may fail).
				cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
				cmd.Env = env
				outBytes, err = cmd.CombinedOutput()
			} else {
				defer devNull.Close()
				cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
				cmd.Env = env
				cmd.Stdin = devNull
				outBytes, err = cmd.CombinedOutput()
			}
			outStr = string(outBytes)
		} else {
			// Default: non-interactive discovery (key-based auth recommended).
			extra := []string{
				"-o", "BatchMode=yes",
				"-o", "StrictHostKeyChecking=accept-new",
				"-o", "ConnectTimeout=5",
			}
			if useMux && strings.TrimSpace(controlPath) != "" {
				extra = append(extra,
					"-o", "ControlMaster=auto",
					"-o", "ControlPersist=10m",
					"-S", controlPath,
				)
			}
			extra = append(extra, "--", spec.Command)
			argv = BuildSSHCommand(r, extra...)
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			outBytes, err = cmd.CombinedOutput()
			outStr = string(outBytes)
		}
		lastOut = outStr

		if ctx.Err() == context.DeadlineExceeded {
			lastErr = fmt.Errorf("discovery timeout running %q", spec.Name)
			continue
		}
		if err != nil {
			lastErr = fmt.Errorf("discovery command %q failed: %v", spec.Name, err)
			continue
		}

		parsed, perr := ParseLLDPOutput(spec.ParserID, r.Host.Name, outStr)
		if perr != nil {
			// Log raw output to per-host daily logs so parsing issues can be debugged without cluttering the UI.
			// This is best-effort and never fails discovery on logging errors.
			if strings.TrimSpace(r.Host.Name) != "" {
				hostKey := strings.TrimSpace(r.Host.Name)
				if li, lerr := EnsureDailyHostLog(hostKey, time.Time{}, LogOptions{}); lerr == nil && strings.TrimSpace(li.Path) != "" {
					f, ferr := os.OpenFile(li.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
					if ferr == nil {
						_, _ = f.WriteString("\n--- network view discovery raw output (parse failed) ---\n")
						_, _ = f.WriteString("host=" + hostKey + "\n")
						_, _ = f.WriteString("spec=" + strings.TrimSpace(spec.Name) + "\n")
						_, _ = f.WriteString("parser=" + strings.TrimSpace(spec.ParserID) + "\n")
						_, _ = f.WriteString("error=" + perr.Error() + "\n")
						_, _ = f.WriteString("----- begin raw -----\n")
						_, _ = f.WriteString(outStr)
						if !strings.HasSuffix(outStr, "\n") {
							_, _ = f.WriteString("\n")
						}
						_, _ = f.WriteString("----- end raw -----\n")
						_ = f.Close()
					}
				}
			}

			lastErr = fmt.Errorf("discovery parse failed for %q: %v", spec.Name, perr)
			continue
		}

		// Log raw output to per-host daily logs for copy/paste debugging (best-effort).
		if strings.TrimSpace(r.Host.Name) != "" {
			hostKey := strings.TrimSpace(r.Host.Name)
			if li, lerr := EnsureDailyHostLog(hostKey, time.Time{}, LogOptions{}); lerr == nil && strings.TrimSpace(li.Path) != "" {
				f, ferr := os.OpenFile(li.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
				if ferr == nil {
					_, _ = f.WriteString("\n--- network view discovery raw output ---\n")
					_, _ = f.WriteString("host=" + hostKey + "\n")
					_, _ = f.WriteString("spec=" + strings.TrimSpace(spec.Name) + "\n")
					_, _ = f.WriteString("parser=" + strings.TrimSpace(spec.ParserID) + "\n")
					_, _ = f.WriteString("entries=" + strconv.Itoa(len(parsed.Entries)) + "\n")
					_, _ = f.WriteString("----- begin raw -----\n")
					_, _ = f.WriteString(outStr)
					if !strings.HasSuffix(outStr, "\n") {
						_, _ = f.WriteString("\n")
					}
					_, _ = f.WriteString("----- end raw -----\n")
					_ = f.Close()
				}
			}
		}

		return &parsed, outStr, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("discovery: no command succeeded")
	}
	// Return the last attempted output (may be empty) for debugging purposes.
	return nil, lastOut, lastErr
}

func (m model) quit() (tea.Model, tea.Cmd) {
	// Do NOT kill tmux panes/windows created during this session.
	//
	// Users expect splits/new windows started by the manager (v/s/w/W, dashboards)
	// to remain alive after the selector exits. Previously, we tracked created
	// panes/windows and killed them on quit, which caused sessions to "disappear"
	// immediately after connecting.
	//
	// If you ever want an explicit cleanup behavior again, make it an opt-in key
	// binding or a config option.
	m2 := m
	m2.quitting = true
	return m2, tea.Quit
}

func (m model) connectOrQuit(r ResolvedHost) (tea.Model, tea.Cmd) {
	// Pre-connect local hooks
	_ = runLocalHooks(r.EffectivePreConnect)

	// Use existing connect helper to honor ExecReplace
	err := connect(r, m.opts.ExecReplace)
	if err != nil {
		m2 := m
		m2.setStatus(fmt.Sprintf("ssh error: %v", err), 2500)
		return m2, nil
	}

	// Post-connect local hooks
	_ = runLocalHooks(r.EffectivePostConnect)

	// Track recents and exit UI (when not replaced)
	m2 := m
	m2.addRecent(r.Host.Name)
	m2.saveState()
	return m2.quit()
}

func (m *model) tmuxSplitH(r ResolvedHost) (string, error) {
	// Build launch line.
	//
	// IMPORTANT:
	// - For askpass: use the internal PTY connector (__connect) so Keychain automation works.
	// - For manual/identity: run the real OpenSSH client directly (`command ssh ...`) to avoid
	//   double tmux-wrapping / wrapper re-entry (especially when users alias ssh->tmux-ssh-manager ssh).
	line := ""
	if strings.EqualFold(strings.TrimSpace(m.effectiveLoginMode(r)), "askpass") {
		bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
		if bin == "" {
			bin = "tmux-ssh-manager"
		}
		userFlag := ""
		if strings.TrimSpace(r.EffectiveUser) != "" {
			userFlag = " --user " + shellEscapeForSh(strings.TrimSpace(r.EffectiveUser))
		}
		line = shellEscapeForSh(bin) + " __connect --host " + shellEscapeForSh(strings.TrimSpace(r.Host.Name)) + userFlag
	} else {
		argv := BuildSSHCommand(r)
		// Bypass any shell alias/function for ssh.
		line = "command " + shellQuoteCmdSimple(argv)
	}

	// Run pre-connect local hooks
	_ = runLocalHooks(r.EffectivePreConnect)

	// Create pane and capture its id so we can enable per-host logging.
	//
	// IMPORTANT (interactive SSH):
	// Do NOT run ssh under an extra `bash -lc` layer when launching panes/windows.
	// That layer can break interactive input (TTY/line discipline) in some tmux environments.
	//
	// Instead:
	// - Print the command for debugging
	// - `exec` the ssh command directly so it becomes the pane's foreground process
	// - If ssh exits non-zero, keep the pane open so the user can read output
	keepOpenLine := "set +e\n" +
		// Run ssh interactively. If it fails immediately, keep pane open with a minimal message.
		"eval " + shellEscapeForSh(line) + "\n" +
		"rc=$?\n" +
		"if [ $rc -ne 0 ]; then\n" +
		"  echo\n" +
		"  echo 'tmux-ssh-manager: connect FAILED (exit='$rc')'\n" +
		"  echo\n" +
		"  echo 'Press Enter to close...'\n" +
		"  read -r _\n" +
		"fi\n" +
		"exit $rc\n"
	cmd := exec.Command("tmux", "split-window", "-h", "-P", "-F", "#{pane_id}", "-c", "#{pane_current_path}", "bash", "-lc", keepOpenLine)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id != "" {
		m.createdPaneIDs = append(m.createdPaneIDs, id)

		// Track pane->host mapping for :send/:dash save.
		if m.paneHost == nil {
			m.paneHost = make(map[string]string)
		}
		m.paneHost[id] = strings.TrimSpace(r.Host.Name)

		// Enable per-host daily logging for this pane using tmux pipe-pane.
		_ = enableTmuxPipePaneLoggingForTarget(id, r.Host.Name)

		// Delay after starting SSH before sending on_connect commands.
		// Use effective per-host delay if configured; otherwise default to 500ms.
		delayMS := r.EffectiveConnectDelayMS
		if delayMS <= 0 {
			delayMS = 500
		}
		time.Sleep(time.Duration(delayMS) * time.Millisecond)

		// Send on_connect commands into the new pane after ssh starts
		// and log them with copy/paste-friendly markers.
		m.sendRecorded(r.Host.Name, id, r.EffectiveOnConnect)

		// Run post-connect local hooks (best-effort: ssh may still be negotiating)
		_ = runLocalHooks(r.EffectivePostConnect)
	}
	return id, nil
}

func (m *model) tmuxSplitV(r ResolvedHost) (string, error) {
	// Build launch line.
	//
	// IMPORTANT:
	// - For askpass: use the internal PTY connector (__connect) so Keychain automation works.
	// - For manual/identity: run the real OpenSSH client directly (`command ssh ...`) to avoid
	//   double tmux-wrapping / wrapper re-entry (especially when users alias ssh->tmux-ssh-manager ssh).
	line := ""
	if strings.EqualFold(strings.TrimSpace(m.effectiveLoginMode(r)), "askpass") {
		bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
		if bin == "" {
			bin = "tmux-ssh-manager"
		}
		userFlag := ""
		if strings.TrimSpace(r.EffectiveUser) != "" {
			userFlag = " --user " + shellEscapeForSh(strings.TrimSpace(r.EffectiveUser))
		}
		line = shellEscapeForSh(bin) + " __connect --host " + shellEscapeForSh(strings.TrimSpace(r.Host.Name)) + userFlag
	} else {
		argv := BuildSSHCommand(r)
		// Bypass any shell alias/function for ssh.
		line = "command " + shellQuoteCmdSimple(argv)
	}

	// Run pre-connect local hooks
	_ = runLocalHooks(r.EffectivePreConnect)

	// Create pane and capture its id so we can enable per-host logging.
	//
	// IMPORTANT (interactive SSH):
	// Avoid nested `bash -lc` execution for ssh; exec the command directly.
	keepOpenLine := "set +e\n" +
		// Run ssh interactively. If it fails immediately, keep pane open with a minimal message.
		"eval " + shellEscapeForSh(line) + "\n" +
		"rc=$?\n" +
		"if [ $rc -ne 0 ]; then\n" +
		"  echo\n" +
		"  echo 'tmux-ssh-manager: connect FAILED (exit='$rc')'\n" +
		"  echo\n" +
		"  echo 'Press Enter to close...'\n" +
		"  read -r _\n" +
		"fi\n" +
		"exit $rc\n"
	cmd := exec.Command("tmux", "split-window", "-v", "-P", "-F", "#{pane_id}", "-c", "#{pane_current_path}", "bash", "-lc", keepOpenLine)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id != "" {
		m.createdPaneIDs = append(m.createdPaneIDs, id)

		// Track pane->host mapping for :send/:dash save.
		if m.paneHost == nil {
			m.paneHost = make(map[string]string)
		}
		m.paneHost[id] = strings.TrimSpace(r.Host.Name)

		// Enable per-host daily logging for this pane using tmux pipe-pane.
		_ = enableTmuxPipePaneLoggingForTarget(id, r.Host.Name)

		// Delay after starting SSH before sending on_connect commands.
		// Use effective per-host delay if configured; otherwise default to 500ms.
		delayMS := r.EffectiveConnectDelayMS
		if delayMS <= 0 {
			delayMS = 500
		}
		time.Sleep(time.Duration(delayMS) * time.Millisecond)

		// Send on_connect commands into the new pane after ssh starts
		// and log them with copy/paste-friendly markers.
		m.sendRecorded(r.Host.Name, id, r.EffectiveOnConnect)

		// Run post-connect local hooks (best-effort: ssh may still be negotiating)
		_ = runLocalHooks(r.EffectivePostConnect)
	}
	return id, nil
}

func (m *model) tmuxNewWindow(r ResolvedHost) (string, error) {
	// Build launch line.
	//
	// IMPORTANT:
	// - For askpass: use the internal PTY connector (__connect) so Keychain automation works.
	// - For manual/identity: run the real OpenSSH client directly (`command ssh ...`) to avoid
	//   double tmux-wrapping / wrapper re-entry (especially when users alias ssh->tmux-ssh-manager ssh).
	line := ""
	if strings.EqualFold(strings.TrimSpace(m.effectiveLoginMode(r)), "askpass") {
		bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
		if bin == "" {
			bin = "tmux-ssh-manager"
		}
		userFlag := ""
		if strings.TrimSpace(r.EffectiveUser) != "" {
			userFlag = " --user " + shellEscapeForSh(strings.TrimSpace(r.EffectiveUser))
		}
		line = shellEscapeForSh(bin) + " __connect --host " + shellEscapeForSh(strings.TrimSpace(r.Host.Name)) + userFlag
	} else {
		argv := BuildSSHCommand(r)
		// Bypass any shell alias/function for ssh.
		line = "command " + shellQuoteCmdSimple(argv)
	}

	// Run pre-connect local hooks
	_ = runLocalHooks(r.EffectivePreConnect)

	// Create window and capture its id so we can enable per-host logging.
	//
	// IMPORTANT (interactive SSH):
	// Avoid nested `bash -lc` execution for ssh; exec the command directly.
	keepOpenLine := "set +e\n" +
		// Run ssh interactively. If it fails immediately, keep window open with a minimal message.
		"eval " + shellEscapeForSh(line) + "\n" +
		"rc=$?\n" +
		"if [ $rc -ne 0 ]; then\n" +
		"  echo\n" +
		"  echo 'tmux-ssh-manager: connect FAILED (exit='$rc')'\n" +
		"  echo\n" +
		"  echo 'Press Enter to close...'\n" +
		"  read -r _\n" +
		"fi\n" +
		"exit $rc\n"
	cmd := exec.Command("tmux", "new-window", "-P", "-F", "#{window_id}", "-c", "#{pane_current_path}", "bash", "-lc", keepOpenLine)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", nil
	}
	m.createdWindowIDs = append(m.createdWindowIDs, id)

	// Find the pane in the new window so we can enable logging and send on_connect commands.
	paneID := ""
	panesOut, perr := exec.Command("tmux", "list-panes", "-t", id, "-F", "#{pane_id}").Output()
	if perr == nil {
		lines := strings.Split(strings.TrimSpace(string(panesOut)), "\n")
		if len(lines) > 0 {
			paneID = strings.TrimSpace(lines[0])
		}
	}
	if paneID != "" {
		// Track pane->host mapping for :send/:dash save.
		if m.paneHost == nil {
			m.paneHost = make(map[string]string)
		}
		m.paneHost[paneID] = strings.TrimSpace(r.Host.Name)

		// Enable per-host daily logging for the new window's pane using tmux pipe-pane.
		_ = enableTmuxPipePaneLoggingForTarget(paneID, r.Host.Name)

		// Delay after starting SSH before sending on_connect commands.
		// Use effective per-host delay if configured; otherwise default to 500ms.
		delayMS := r.EffectiveConnectDelayMS
		if delayMS <= 0 {
			delayMS = 500
		}
		time.Sleep(time.Duration(delayMS) * time.Millisecond)

		// Send on_connect commands and log them with copy/paste-friendly markers.
		m.sendRecorded(r.Host.Name, paneID, r.EffectiveOnConnect)
	}

	// Run post-connect local hooks (best-effort: ssh may still be negotiating)
	_ = runLocalHooks(r.EffectivePostConnect)

	return paneID, nil
}

// effectiveLoginMode resolves the effective login mode for a host.
//
// Precedence (highest first):
// 1) Per-host extras auth_mode (stored locally under ~/.config/tmux-ssh-manager/hosts/<host>.conf)
//   - auth_mode=keychain -> askpass
//   - auth_mode=manual   -> manual
//
// 2) YAML host login_mode (manual/askpass)
//
// This lets the TUI persist login behavior without rewriting the YAML.
func (m *model) effectiveLoginMode(r ResolvedHost) string {
	hostKey := strings.TrimSpace(r.Host.Name)
	if hostKey != "" {
		if ex, err := LoadHostExtras(hostKey); err == nil {
			am := strings.ToLower(strings.TrimSpace(ex.AuthMode))
			switch am {
			case "keychain":
				return "askpass"
			case "manual":
				return "manual"
			}
		}
	}

	lm := strings.ToLower(strings.TrimSpace(r.Host.LoginMode))
	if lm == "" {
		return "manual"
	}
	return lm
}

// (Askpass wrapper removed)
//
// The project previously attempted to use SSH_ASKPASS from within tmux splits/windows.
// This proved brittle across environments (especially with keyboard-interactive prompts).
//
// We now prefer a PTY-mediated, expect-like connector via the internal `tmux-ssh-manager __connect`
// helper, which runs ssh under a PTY and supplies the Keychain password when prompted.
//
// Keeping the old askpass wrapper/parsing code around is confusing and risks regressions,
// so it has been removed.
//
// sanitizeLogLine removes control characters that make pipe-pane logs noisy when viewed.
// We keep the raw log on disk; sanitization is applied at view time only.
func sanitizeLogLine(s string) string {
	// Normalize CRLF / carriage-return prompts so they don't show up as ^M.
	s = strings.ReplaceAll(s, "\r", "")

	// Strip ANSI escape sequences:
	// - CSI: ESC [ ... letter
	// - OSC: ESC ] ... BEL or ST
	// - Charset select: ESC ( B  (and similar)
	//
	// This is intentionally simple and covers the common sequences seen in prompts.
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // ESC
			// CSI
			if i+1 < len(s) && s[i+1] == '[' {
				i += 2
				// Parameter bytes: 0x30-0x3f, Intermediate: 0x20-0x2f, Final: 0x40-0x7e
				for i < len(s) {
					b := s[i]
					if b >= 0x40 && b <= 0x7e {
						i++
						break
					}
					i++
				}
				continue
			}
			// OSC
			if i+1 < len(s) && s[i+1] == ']' {
				i += 2
				for i < len(s) {
					if s[i] == 0x07 { // BEL
						i++
						break
					}
					// ST: ESC \
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				continue
			}
			// Charset select: ESC ( X
			if i+2 < len(s) && s[i+1] == '(' {
				i += 3
				continue
			}
			// Unknown ESC sequence: skip ESC itself
			i++
			continue
		}

		// Drop other control chars except tab.
		if s[i] < 0x20 && s[i] != '\t' {
			i++
			continue
		}

		out = append(out, rune(s[i]))
		i++
	}
	return string(out)
}

// tmuxInstallMyKey launches an "install local public key to remote authorized_keys" workflow
// in a new tmux window (preferred). If that fails, it falls back to a split pane.
// If replace is true, it replaces authorized_keys (DANGEROUS) - should only be used after explicit confirmation.
func (m *model) tmuxInstallMyKey(r ResolvedHost, replace bool) error {
	// Pick a key: prefer per-host extras config if present; otherwise use best local default.
	hostKey := strings.TrimSpace(r.Host.Name)
	extras, _ := LoadHostExtras(hostKey)

	mode := KeyInstallEnsure
	if replace {
		mode = KeyInstallReplace
	} else if strings.EqualFold(strings.TrimSpace(extras.AuthorizedKeysMode), "replace") {
		// Do not auto-replace just because config says so. Replacement must be explicitly requested.
		mode = KeyInstallEnsure
	}

	var pub LocalPublicKey
	var err error
	if strings.TrimSpace(extras.KeyInstallPubKeyPath) != "" {
		pub, err = ReadLocalPublicKey(extras.KeyInstallPubKeyPath)
		if err != nil {
			// If a configured path is missing/unreadable, fall back to auto-detection
			// instead of failing outright. This handles setups where ssh defaults to
			// id_ed25519 but id_rsa.pub isn't present.
			keys, derr := DetectLocalPublicKeys()
			if derr != nil {
				return derr
			}
			if len(keys) == 0 {
				return fmt.Errorf("no local public keys found under ~/.ssh (configured key_install_pubkey=%s)", strings.TrimSpace(extras.KeyInstallPubKeyPath))
			}
			pub = keys[0]
		}
	} else {
		// Default: Linux-oriented baseline key. Prefer ~/.ssh/id_rsa.pub unless explicitly overridden per host.
		// If it's missing/unreadable, fall back to auto-detection (ed25519, ecdsa, rsa, etc.).
		pub, err = ReadLocalPublicKey("~/.ssh/id_rsa.pub")
		if err != nil {
			keys, derr := DetectLocalPublicKeys()
			if derr != nil {
				return derr
			}
			if len(keys) == 0 {
				return fmt.Errorf("no local public keys found under ~/.ssh (expected id_rsa.pub or other *.pub)")
			}
			pub = keys[0]
		}
	}

	// Determine connect strategy:
	// - If keychain auth is enabled AND a credential is available, run the install non-interactively by using
	//   OpenSSH with SSH_ASKPASS pointing at tmux-ssh-manager's internal __askpass helper.
	//   This avoids the interactive PTY connector (__connect), so the install completes immediately (no "exit" needed).
	// - Otherwise, fall back to plain ssh (you may be prompted).
	loginMode := strings.ToLower(strings.TrimSpace(m.effectiveLoginMode(r)))
	credOK := false
	if loginMode == "askpass" {
		if err := CredGet(hostKey, r.EffectiveUser, "password"); err == nil {
			credOK = true
		}
	}

	// Build the remote authorized_keys install script via the shared helper (avoid duplicating logic here).
	remoteScript, err := BuildAuthorizedKeysInstallRemoteScript(pub, mode)
	if err != nil {
		return err
	}
	// Avoid: "sh: 0: -c requires an argument"
	//
	// Some environments/log wrappers can end up invoking `sh -c` without the script argument if quoting gets
	// mangled across layers. To make this robust, execute the remote script via stdin:
	//
	//   ssh ... sh -s <<'EOF'
	//   <script>
	//   EOF
	//
	// NOTE: Do NOT insert a `--` separator before the remote command.
	// macOS/OpenSSH treats `--` as an invalid option for ssh ("illegal option -- -").
	remoteHeredoc := "sh -s <<'TSSM_EOF'\n" + remoteScript + "\nTSSM_EOF\n"

	// Compute ssh destination + flags from resolved host (only when present / non-default):
	// - Port: include only if non-default and >0
	// - ProxyJump: include only if set
	// - IdentityFile: include only if set (per-host extras or explicit override)
	dest := strings.TrimSpace(r.Host.Name)
	if strings.TrimSpace(r.EffectiveUser) != "" {
		dest = strings.TrimSpace(r.EffectiveUser) + "@" + dest
	}
	if dest == "" {
		return fmt.Errorf("key install: empty destination")
	}

	// Best-effort identity override: honor per-host extras identity_file when set.
	identityFlag := ""
	if ex, exErr := LoadHostExtras(hostKey); exErr == nil {
		if id := strings.TrimSpace(ex.IdentityFile); id != "" {
			id = expandPath(id)
			if id != "" {
				identityFlag = " -i " + shellEscapeForSh(id)
			}
		}
	}

	portFlag := ""
	if r.EffectivePort > 0 && r.EffectivePort != 22 {
		portFlag = " -p " + shellEscapeForSh(strconv.Itoa(r.EffectivePort))
	}

	jumpFlag := ""
	if strings.TrimSpace(r.EffectiveJumpHost) != "" {
		jumpFlag = " -J " + shellEscapeForSh(strings.TrimSpace(r.EffectiveJumpHost))
	}

	// This is the computed OpenSSH line (alias-proof when prefixed with `command` below).
	//
	// IMPORTANT:
	// Do NOT prefix this with "command " at call sites by concatenating strings. "command" is a shell builtin,
	// and writing "command ssh ..." is fine, but writing "command " + sshComputed becomes "command ssh ..."
	// only if sshComputed begins with "ssh". Keep sshComputed starting with "ssh" and call sites should run:
	//   command ssh ...
	sshComputed := "ssh" + portFlag + jumpFlag + identityFlag + " " + shellEscapeForSh(dest) + " " + shellEscapeForSh(remoteHeredoc)

	bin := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_BIN"))
	if bin == "" {
		bin = "tmux-ssh-manager"
	}

	// Human-friendly banner shown in the window before running the install.
	modeLabel := "ensure"
	if mode == KeyInstallReplace {
		modeLabel = "replace"
	}
	explain := strings.Join([]string{
		"set -e",
		fmt.Sprintf("echo 'tmux-ssh-manager: key install (%s)'", modeLabel),
		fmt.Sprintf("echo '  host: %s'", shellEscapeForSh(hostKey)),
		fmt.Sprintf("echo '  user: %s'", shellEscapeForSh(strings.TrimSpace(r.EffectiveUser))),
		fmt.Sprintf("echo '  pubkey: %s'", shellEscapeForSh(pub.Path)),
		fmt.Sprintf("echo '  login_mode: %s'", shellEscapeForSh(loginMode)),
		fmt.Sprintf("echo '  credential: %s'", shellEscapeForSh(func() string {
			if credOK {
				return "available"
			}
			return "missing/unknown"
		}())),
		"echo",
		"echo 'What happens:'",
		"echo '  - We connect to the host and run a remote script to update ~/.ssh/authorized_keys.'",
		"echo '  - Mode ensure is idempotent: it appends the key only if missing.'",
		"echo '  - If login_mode=askpass and a Keychain credential exists, we run the install non-interactively via SSH_ASKPASS (no manual exit required).'",
		"echo",
	}, "\n")

	var cmdLine string
	if loginMode == "askpass" && credOK {
		// Non-interactive install using OpenSSH + SSH_ASKPASS.
		//
		// - We create a temp wrapper script so OpenSSH can exec SSH_ASKPASS reliably.
		//   (This mirrors the scp wrapper approach.)
		// - We must bypass user aliases/functions for ssh: use `command ssh`.
		// - After successful install, automatically flip the host to identity-based auth by:
		//   - setting identity_file=~/.ssh/id_rsa if not set
		//   - setting auth_mode=manual (stop forcing keychain)
		userFlag := ""
		if strings.TrimSpace(r.EffectiveUser) != "" {
			userFlag = " --user " + shellEscapeForSh(strings.TrimSpace(r.EffectiveUser))
		}

		cmdLine = explain + "\n" +
			"TMPDIR=\"${TMPDIR:-/tmp}\"\n" +
			"WRAP=\"$TMPDIR/tssm-askpass-$$.sh\"\n" +
			"umask 077\n" +
			"cat >\"$WRAP\" <<'EOF'\n" +
			"#!/bin/sh\n" +
			"exec " + shellEscapeForSh(bin) + " __askpass --host " + shellEscapeForSh(hostKey) + userFlag + " --kind password\n" +
			"EOF\n" +
			"chmod 700 \"$WRAP\"\n" +
			"export SSH_ASKPASS=\"$WRAP\"\n" +
			"export SSH_ASKPASS_REQUIRE=force\n" +
			"export DISPLAY=1\n" +
			"set +e\n" +
			"command " + sshComputed + "\n" +
			"rc=$?\n" +
			"rm -f \"$WRAP\" >/dev/null 2>&1 || true\n" +
			"set -e\n" +
			"echo\n" +
			"if [ $rc -eq 0 ]; then\n" +
			"  echo 'SUCCESS: authorized_keys updated.'\n" +
			"  echo 'Auto-switching this host to identity-based auth...'\n" +
			"  # Persist identity_file default (if unset) and disable keychain forcing.\n" +
			"  # Do NOT edit the conf file with grep/sed/perl here; delegate to the Go code path so\n" +
			"  # normalization and file IO are consistent.\n" +
			"  " + shellEscapeForSh(bin) + " __hostextras-switch-to-identity --host " + shellEscapeForSh(hostKey) + "\n" +
			"  echo 'OK: auth_mode set to manual; identity_file ensured.'\n" +
			"else\n" +
			"  echo \"FAILED (exit=$rc)\"\n" +
			"fi\n" +
			"echo\n" +
			"echo 'Press Enter to close...'\n" +
			"read -r _\n" +
			"exit $rc"
	} else {
		// Plain ssh path (may prompt).
		//
		// IMPORTANT:
		// Users may alias `ssh` to `tmux-ssh-manager ssh`. When we truly want the OpenSSH client,
		// bypass aliases/functions by using the shell builtin `command`:
		//   command ssh ...
		cmdLine = explain + "\n" +
			"command " + sshComputed + "\n" +
			"rc=$?; echo; if [ $rc -eq 0 ]; then echo 'SUCCESS: authorized_keys updated.'; else echo \"FAILED (exit=$rc)\"; fi; echo; echo 'Press Enter to close...'; read -r _; exit $rc"
	}

	// Prefer opening in a new tmux window so interactive prompts are clean.
	// If new-window fails, fall back to split.
	cmd := exec.Command("tmux", "new-window", "-c", "#{pane_current_path}", "bash", "-lc", cmdLine)
	if err := cmd.Run(); err == nil {
		return nil
	}

	// Fallback: split vertically
	cmd2 := exec.Command("tmux", "split-window", "-v", "-c", "#{pane_current_path}", "bash", "-lc", cmdLine)
	return cmd2.Run()
}

func (m model) tmuxYank(text string) error {
	if err := exec.Command("tmux", "set-buffer", "--", text).Run(); err != nil {
		return err
	}
	_ = exec.Command("tmux", "display-message", "-d", "1500", "Yanked SSH command to tmux buffer").Run()
	return nil
}

func isDigitKey(k string) bool {
	return len(k) == 1 && k[0] >= '0' && k[0] <= '9'
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

func boolPtr(v bool) *bool { return &v }

// shellQuoteCmdSimple quotes argv for a Bourne shell line.
// This is a simple single-quote strategy suitable for command previews and tmux.
func shellQuoteCmdSimple(argv []string) string {
	isSpecial := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '"', '\'', '\\', '$', '`', '&', '|', ';', '<', '>', '(', ')', '{', '}', '*', '?', '!', '~', '#':
			return true
		default:
			return false
		}
	}
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		if a == "" {
			quoted = append(quoted, "''")
			continue
		}
		if strings.IndexFunc(a, isSpecial) >= 0 {
			quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", "'\"'\"'")+"'")
		} else {
			quoted = append(quoted, a)
		}
	}
	return strings.Join(quoted, " ")
}

func sendOnConnectToPane(paneID string, cmds []string) {
	if paneID == "" || len(cmds) == 0 {
		return
	}
	for _, c := range cmds {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_ = exec.Command("tmux", "send-keys", "-t", paneID, "--", c, "Enter").Run()
	}
}

// sendOnConnectToPaneLogged sends commands to a tmux pane and also writes copy/paste-friendly
// markers to the per-host daily log. Each marker begins on its own line and ends with a newline.
//
// Marker format:
//
//	>>> <command>
//
// Notes:
//   - We do NOT include pane id to keep logs cleaner.
//   - Only commands we send programmatically are logged (dashboards/on_connect), which avoids
//     logging interactive user typing (and potential secrets).
func sendOnConnectToPaneLogged(hostKey, paneID string, cmds []string) {
	hostKey = strings.TrimSpace(hostKey)
	if paneID == "" || len(cmds) == 0 {
		return
	}
	for _, c := range cmds {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}

		// Record commands explicitly into the host log (separate from SSH output).
		_, _ = AppendHostLogLine(hostKey, time.Now(), DefaultLogOptions(), ">>> "+c)

		_ = exec.Command("tmux", "send-keys", "-t", paneID, "--", c, "Enter").Run()
	}
}

// sendRecorded wraps sendOnConnectToPaneLogged and, if recording is enabled,
// records the (host, command list) mapping keyed by pane id.
// Note: this only captures commands that tmux-ssh-manager itself sends.
// It does not (and cannot) capture arbitrary keystrokes typed inside SSH.
func (m *model) sendRecorded(hostKey, paneID string, cmds []string) {
	// Always send to pane first (best-effort).
	sendOnConnectToPaneLogged(hostKey, paneID, cmds)

	if m == nil || !m.recording {
		return
	}
	hostKey = strings.TrimSpace(hostKey)
	paneID = strings.TrimSpace(paneID)
	if hostKey == "" || paneID == "" || len(cmds) == 0 {
		return
	}
	if m.recordedPanes == nil {
		m.recordedPanes = make(map[string]*RecordedPane)
	}

	rp := m.recordedPanes[paneID]
	if rp == nil {
		rp = &RecordedPane{Host: hostKey}
		m.recordedPanes[paneID] = rp
	} else if strings.TrimSpace(rp.Host) == "" {
		rp.Host = hostKey
	}

	for _, c := range cmds {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		rp.Commands = append(rp.Commands, c)
	}
}

// runLocalHooks executes a list of local shell commands (pre/post-connect hooks).
// Commands run via "bash -lc" to honor shell configuration and PATH.
func runLocalHooks(cmds []string) error {
	if len(cmds) == 0 {
		return nil
	}
	for _, c := range cmds {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if err := exec.Command("bash", "-lc", c).Run(); err != nil {
			return err
		}
	}
	return nil
}

// enableTmuxPipePaneLoggingForTarget enables per-host daily logging for a specific tmux pane.
// It appends to the per-host daily log file:
//
//	~/.config/tmux-ssh-manager/logs/<hostkey>/YYYY-MM-DD.log
//
// This uses `tmux pipe-pane -t <pane_id> -o "<cmd>"` so it does not break the interactive TTY.
//
// Important notes:
//   - tmux expects the pipe command as a single argument string.
//   - Avoid extra shell layers ("sh -lc ...") to reduce quoting/parsing issues.
//   - Avoid quoting the log path inside the pipe command; we control the path and it should not
//     contain spaces. (Host keys are sanitized; base dir is stable.)
//   - After enabling pipe-pane, emit a test marker into the pane using tmux display-message.
//     If that marker does not show up in the log file, logging is not actually working.
//
// Best-effort: callers may ignore errors, but this function records failures to the host log.
func enableTmuxPipePaneLoggingForTarget(paneID, hostKey string) error {
	paneID = strings.TrimSpace(paneID)
	hostKey = strings.TrimSpace(hostKey)
	if paneID == "" || hostKey == "" {
		return nil
	}

	opts := DefaultLogOptions()

	// Ensure log exists and get path
	info, err := EnsureDailyHostLog(hostKey, time.Now(), opts)
	if err != nil {
		return err
	}

	// Use socket-aware tmux wrapper to avoid client/server context mismatches (e.g., popups).
	if err := EnablePipePaneAppend(paneID, info.Path); err != nil {
		_, _ = AppendHostLogLine(hostKey, time.Now(), opts,
			fmt.Sprintf("log: failed to enable tmux pipe-pane for pane %s: %v", paneID, err))
		return err
	}

	// Verify best-effort and record state.
	if on, pipePath, vErr := VerifyPipePane(paneID); vErr == nil {
		if !on {
			_, _ = AppendHostLogLine(hostKey, time.Now(), opts,
				fmt.Sprintf("log: pipe-pane verify failed for pane %s (pane_pipe=0)", paneID))
		} else {
			if pipePath != "" {
				_, _ = AppendHostLogLine(hostKey, time.Now(), opts,
					fmt.Sprintf("log: pipe-pane verify ok for pane %s (path=%s)", paneID, pipePath))
			}
		}
	}

	_, _ = AppendHostLogLine(hostKey, time.Now(), opts,
		fmt.Sprintf("log: enabled tmux pipe-pane for pane %s -> %s", paneID, info.Path))
	return nil
}

// shellEscapeForSh escapes a string for safe use in a shell command line.
// This is a minimal single-quote escape strategy.
// Example: /tmp/foo'bar -> '/tmp/foo'"'"'bar'
func shellEscapeForSh(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexByte(s, '\'') < 0 {
		return "'" + s + "'"
	}
	// Close/open quotes around single quotes: 'foo'"'"'bar'
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// sanitizeNameForSession converts a user-facing name into a tmux-safe session identifier.
// Keep it consistent with tmux-session-manager's exporter sanitizer (lowercase, [_a-z0-9], underscores).
func sanitizeNameForSession(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastUnderscore = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case r == '-' || r == '_':
			b.WriteRune('_')
			lastUnderscore = true
		default:
			if !lastUnderscore {
				b.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "session"
	}
	return out
}

// --- Logs viewer ---
//
// The logs viewer reads per-host daily logs stored under:
//
//	~/.config/tmux-ssh-manager/logs/<hostkey>/YYYY-MM-DD.log
//
// Keybindings (vim-ish):
// - q / Esc: close logs view
// - j/k: down/up one line
// - d/u: half-page down/up
// - gg: top, G: bottom
// - J/K: next/prev log file (by date, newest first)
// - r: reload current view window
func (m *model) openLogs(hostKey string) error {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return fmt.Errorf("empty host key")
	}

	// Determine today's log file path (created if missing).
	// We compute the default path here rather than depending on other packages.
	base := os.Getenv("XDG_CONFIG_HOME")
	if strings.TrimSpace(base) == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	logDir := filepath.Join(base, "tmux-ssh-manager", "logs", sanitizeHostKeyToFilename(hostKey))
	_ = os.MkdirAll(logDir, 0o700)

	today := time.Now().Format("2006-01-02")
	todayPath := filepath.Join(logDir, today+".log")
	// Touch file so it's always readable
	if f, err := os.OpenFile(todayPath, os.O_CREATE, 0o600); err == nil {
		_ = f.Close()
	}

	// Collect log files (newest first by filename)
	entries, _ := os.ReadDir(logDir)
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".log") {
			files = append(files, filepath.Join(logDir, name))
		}
	}
	// Ensure today's file is included
	foundToday := false
	for _, p := range files {
		if filepath.Clean(p) == filepath.Clean(todayPath) {
			foundToday = true
			break
		}
	}
	if !foundToday {
		files = append(files, todayPath)
	}
	sort.Slice(files, func(i, j int) bool {
		ai := filepath.Base(files[i])
		aj := filepath.Base(files[j])
		return ai > aj
	})

	m.logHostKey = hostKey
	m.logFiles = files
	m.logSelected = 0
	m.logFilePath = files[0]
	m.logStartLine = 0
	return m.reloadLogsWindow()
}

func (m *model) reloadLogsWindow() error {
	if strings.TrimSpace(m.logFilePath) == "" {
		m.logLines = nil
		m.logTotalLines = 0
		return nil
	}

	// Determine window size (leave room for header/footer)
	h := m.height
	if h <= 0 {
		h = 24
	}
	// header(3) + footer(2) slack
	page := h - 5
	if page < 5 {
		page = 5
	}

	lines, total, err := ReadLogWindow(m.logFilePath, m.logStartLine, page)
	if err != nil {
		return err
	}

	// Sanitize for in-TUI viewing so control sequences don't clutter the display.
	for i := range lines {
		lines[i] = sanitizeLogLine(lines[i])
	}

	m.logLines = lines
	m.logTotalLines = total
	return nil
}

func (m *model) viewLogs() string {
	var b strings.Builder

	// Header
	host := m.logHostKey
	file := filepath.Base(m.logFilePath)
	b.WriteString(fmt.Sprintf("Logs: %s  (%s)\n", host, file))
	b.WriteString("j/k line  d/u half-page  gg/G top/bot  J/K file  r reload  q close\n")
	b.WriteString(strings.Repeat("-", maxInt(10, m.width)) + "\n")

	// Body
	for _, ln := range m.logLines {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if len(m.logLines) == 0 {
		b.WriteString("(no log lines)\n")
	}

	// Footer
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("File %d/%d  StartLine=%d  TotalLines~%d\n",
		m.logSelected+1, maxInt(1, len(m.logFiles)), m.logStartLine, m.logTotalLines))

	return b.String()
}

// handleLogsKeys processes keypresses when logs viewer is active.
// Returns (handled=true) if it consumed the key.
func (m *model) handleLogsKeys(k tea.KeyMsg) (handled bool, out *model, cmd tea.Cmd) {
	key := k.String()

	switch key {
	case "enter":
		// In logs viewer, Enter should never fall through to connect.
		// Treat it as "close logs and return to host list".
		m.showLogs = false
		m.logHostKey = ""
		m.logFilePath = ""
		m.logFiles = nil
		m.logSelected = 0
		m.logStartLine = 0
		m.logLines = nil
		m.logTotalLines = 0
		return true, m, nil
	case "q", "esc":
		m.showLogs = false
		m.logHostKey = ""
		m.logFilePath = ""
		m.logFiles = nil
		m.logSelected = 0
		m.logStartLine = 0
		m.logLines = nil
		m.logTotalLines = 0
		return true, m, nil
	case "r":
		_ = m.reloadLogsWindow()
		return true, m, nil
	case "j", "down":
		m.logStartLine++
		_ = m.reloadLogsWindow()
		return true, m, nil
	case "k", "up":
		if m.logStartLine > 0 {
			m.logStartLine--
		}
		_ = m.reloadLogsWindow()
		return true, m, nil
	case "d", "pgdown":
		// half page down
		step := maxInt(1, (m.height-5)/2)
		m.logStartLine += step
		_ = m.reloadLogsWindow()
		return true, m, nil
	case "u", "pgup":
		step := maxInt(1, (m.height-5)/2)
		m.logStartLine -= step
		if m.logStartLine < 0 {
			m.logStartLine = 0
		}
		_ = m.reloadLogsWindow()
		return true, m, nil
	case "G":
		// crude bottom: jump near end
		if m.logTotalLines > 0 {
			m.logStartLine = maxInt(0, m.logTotalLines-(m.height-5))
		}
		_ = m.reloadLogsWindow()
		return true, m, nil
	case "g":
		// support gg
		// defer to Update loop pendingG behavior: here we just handle a single 'g' by toggling pendingG
		// (we reuse existing pendingG in model)
		if m.pendingG {
			m.pendingG = false
			m.logStartLine = 0
			_ = m.reloadLogsWindow()
		} else {
			m.pendingG = true
		}
		return true, m, nil
	case "J":
		// next file (older)
		if len(m.logFiles) > 0 && m.logSelected < len(m.logFiles)-1 {
			m.logSelected++
			m.logFilePath = m.logFiles[m.logSelected]
			m.logStartLine = 0
			_ = m.reloadLogsWindow()
		}
		return true, m, nil
	case "K":
		// prev file (newer)
		if len(m.logFiles) > 0 && m.logSelected > 0 {
			m.logSelected--
			m.logFilePath = m.logFiles[m.logSelected]
			m.logStartLine = 0
			_ = m.reloadLogsWindow()
		}
		return true, m, nil
	}

	// Any other key: no-op
	return false, m, nil
}
