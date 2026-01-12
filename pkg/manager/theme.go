package manager

import (
	"encoding/json"

	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Theme provides optional colorized rendering for the TUI.
// It is dependency-free (no external styling libs) and uses ANSI escape sequences.
// All hooks are safe to call even when theming is disabled; they fall back to plain strings.
//
// Configuration sources (in priority order):
// 1) Explicit JSON path passed to LoadTheme(path)
// 2) ~/.config/tmux-ssh-manager/theme.json (or $XDG_CONFIG_HOME/tmux-ssh-manager/theme.json)
// 3) Env var TMUX_SSH_MANAGER_THEME = none | dark | light | catppuccin | catppuccin-mocha
// 4) Auto-defaults (enabled if terminal supports color)
//
// JSON structure (all fields optional):
// {
//   "enabled": true,
//   "name": "catppuccin-mocha",
//   "colors": {
//     "header": "bold mauve",
//     "accent": "teal",
//     "selected": "bold peach",
//     "favorite": "yellow",
//     "checkbox": "blue",
//     "group": "lavender",
//     "dim": "faint",
//     "separator": "gray",
//     "help": "teal",
//     "error": "red",
//     "success": "green",
//     "warn": "yellow"
//   }
// }
type Theme struct {
	Enabled bool

	Header    string
	Accent    string
	Selected  string
	Favorite  string
	Checkbox  string
	Group     string
	Dim       string
	Separator string
	Help      string
	Error     string
	Success   string
	Warn      string
}

// ThemeFile is the on-disk JSON representation.
type ThemeFile struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Name    string            `json:"name,omitempty"`
	Colors  map[string]string `json:"colors,omitempty"`
}

// ---------- Public API ----------

// LoadTheme resolves theming by trying the provided path, then default path,
// then environment variable TMUX_SSH_MANAGER_THEME, and finally automatic defaults.
// If anything fails, a safe default theme is returned.
func LoadTheme(explicitPath string) Theme {
	// Priority 1: explicit path
	if strings.TrimSpace(explicitPath) != "" {
		if t, err := loadThemeFromFile(explicitPath); err == nil {
			return t
		}
	}
	// Priority 2: default file
	if p, err := defaultThemePath(); err == nil {
		if t, err := loadThemeFromFile(p); err == nil {
			return t
		}
	}
	// Priority 3: environment
	if v := strings.TrimSpace(os.Getenv("TMUX_SSH_MANAGER_THEME")); v != "" {
		switch strings.ToLower(v) {
		case "none", "off", "disabled":
			return NoTheme()
		case "catppuccin", "catppuccin-mocha", "mocha":
			return CatppuccinMochaTheme()
		case "light":
			return LightTheme()
		case "dark":
			return DarkTheme()
		default:
			// Try parsing as "key=value; ..." or raw mapping string if provided
			// Otherwise, fall through to auto.
		}
	}
	// Priority 4: auto-detect
	return AutoTheme()
}

// NoTheme disables all ANSI styling.
func NoTheme() Theme {
	return Theme{Enabled: false}
}

// AutoTheme enables theming whenever the terminal likely supports color.
func AutoTheme() Theme {
	if !terminalSupportsColor() {
		return NoTheme()
	}
	return DarkTheme()
}

// DarkTheme provides a sane default palette for dark terminals.
func DarkTheme() Theme {
	return Theme{
		Enabled:   true,
		Header:    seq("1"),          // bold
		Accent:    seq("36"),         // cyan
		Selected:  seq("1;97"),       // bold bright white
		Favorite:  seq("33"),         // yellow
		Checkbox:  seq("34"),         // blue
		Group:     seq("35"),         // magenta
		Dim:       seq("2"),          // faint
		Separator: seq("90"),         // bright black (gray)
		Help:      seq("36"),         // cyan
		Error:     seq("31"),         // red
		Success:   seq("32"),         // green
		Warn:      seq("33"),         // yellow
	}
}

// LightTheme provides a default palette for light terminals.
func LightTheme() Theme {
	return Theme{
		Enabled:   true,
		Header:    seq("1"),          // bold
		Accent:    seq("34"),         // blue
		Selected:  seq("1;30"),       // bold black
		Favorite:  seq("33"),         // yellow
		Checkbox:  seq("34"),         // blue
		Group:     seq("35"),         // magenta
		Dim:       seq("2"),          // faint
		Separator: seq("90"),         // gray
		Help:      seq("34"),         // blue
		Error:     seq("31"),         // red
		Success:   seq("32"),         // green
		Warn:      seq("33"),         // yellow
	}
}

// CatppuccinMochaTheme approximates Catppuccin Mocha colors with 256-color codes.
func CatppuccinMochaTheme() Theme {
	// Approx mappings:
	// mauve 183, lavender 147, peach 216, teal 37/44/43, text 252, subtext 245
	return Theme{
		Enabled:   true,
		Header:    seq("1;38;5;183"), // bold mauve
		Accent:    seq("38;5;44"),    // teal-ish
		Selected:  seq("1;38;5;216"), // bold peach
		Favorite:  seq("38;5;221"),   // yellow-ish
		Checkbox:  seq("38;5;39"),    // blue-ish
		Group:     seq("38;5;147"),   // lavender
		Dim:       seq("38;5;245"),   // muted gray
		Separator: seq("38;5;240"),   // darker gray
		Help:      seq("38;5;44"),    // teal-ish
		Error:     seq("38;5;203"),   // red-ish
		Success:   seq("38;5;114"),   // green-ish
		Warn:      seq("38;5;215"),   // peach-yellow
	}
}

// HeaderLine applies header styling.
func (t Theme) HeaderLine(s string) string   { return t.apply(t.Header, s) }
func (t Theme) AccentText(s string) string   { return t.apply(t.Accent, s) }
func (t Theme) SelectedText(s string) string { return t.apply(t.Selected, s) }
func (t Theme) DimText(s string) string      { return t.apply(t.Dim, s) }
func (t Theme) HelpText(s string) string     { return t.apply(t.Help, s) }
func (t Theme) ErrorText(s string) string    { return t.apply(t.Error, s) }
func (t Theme) SuccessText(s string) string  { return t.apply(t.Success, s) }
func (t Theme) WarnText(s string) string     { return t.apply(t.Warn, s) }

// Hooks for common TUI fragments:

// SelectedPrefix returns a colored " > " or "   " prefix.
func (t Theme) SelectedPrefix(selected bool) string {
	if !selected {
		return "   "
	}
	return t.apply(t.Selected, " > ")
}

// CheckboxMark renders a colored checkbox.
func (t Theme) CheckboxMark(on bool) string {
	if on {
		return t.apply(t.Checkbox, "[x]")
	}
	return t.apply(t.Dim, "[ ]")
}

// FavoriteStar renders a colored star (★) or a space.
func (t Theme) FavoriteStar(on bool) string {
	if on {
		return t.apply(t.Favorite, "★")
	}
	return " "
}

// Separator returns a colored column separator (│).
func (t Theme) SeparatorRune() string {
	return t.apply(t.Separator, "│")
}

// ListLine composes a colored list row.
// Example usage in a list view:
//   line := theme.ListLine(i, selected, checked, favorited, display)
func (t Theme) ListLine(index int, selected, checked, favorited bool, content string) string {
	prefix := t.SelectedPrefix(selected)
	cb := t.CheckboxMark(checked)
	star := t.FavoriteStar(favorited)
	line := fmt.Sprintf("%s%2d) %s %s %s", prefix, index, cb, star, content)
	if selected {
		return t.apply(t.Selected, line)
	}
	return line
}

// ---------- Implementation ----------

func (t Theme) apply(seqCode, s string) string {
	if !t.Enabled || strings.TrimSpace(seqCode) == "" || s == "" {
		return s
	}
	return "\x1b[" + seqCode + "m" + s + "\x1b[0m"
}

func terminalSupportsColor() bool {
	// Respect NO_COLOR https://no-color.org/
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	if term == "" || term == "dumb" {
		return false
	}
	// If stdout is redirected, we still allow color; Bubble Tea typically manages alternate screen.
	// Accept common color-capable terms:
	for _, token := range []string{"color", "ansi", "xterm", "screen", "tmux", "rxvt"} {
		if strings.Contains(term, token) {
			return true
		}
	}
	// Default to true in interactive contexts
	return true
}

func defaultThemePath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "theme.json"), nil
}

func loadThemeFromFile(path string) (Theme, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Theme{}, err
	}
	var tf ThemeFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return Theme{}, err
	}
	// Base by name
	var base Theme
	switch strings.ToLower(strings.TrimSpace(tf.Name)) {
	case "none", "off", "disabled":
		base = NoTheme()
	case "catppuccin", "catppuccin-mocha", "mocha":
		base = CatppuccinMochaTheme()
	case "light":
		base = LightTheme()
	case "dark":
		base = DarkTheme()
	case "":
		base = AutoTheme()
	default:
		// Unknown name -> default base
		base = AutoTheme()
	}
	// Enabled override
	if tf.Enabled != nil {
		base.Enabled = *tf.Enabled
	}
	// Apply color overrides
	if tf.Colors != nil {
		applyColorOverride := func(key string, dst *string) {
			if v, ok := tf.Colors[key]; ok {
				if seqStr := parseStyleSequence(v); seqStr != "" {
					*dst = seqStr
				}
			}
		}
		applyColorOverride("header", &base.Header)
		applyColorOverride("accent", &base.Accent)
		applyColorOverride("selected", &base.Selected)
		applyColorOverride("favorite", &base.Favorite)
		applyColorOverride("checkbox", &base.Checkbox)
		applyColorOverride("group", &base.Group)
		applyColorOverride("dim", &base.Dim)
		applyColorOverride("separator", &base.Separator)
		applyColorOverride("help", &base.Help)
		applyColorOverride("error", &base.Error)
		applyColorOverride("success", &base.Success)
		applyColorOverride("warn", &base.Warn)
	}
	return base, nil
}

// parseStyleSequence converts a user-friendly description into an ANSI SGR sequence.
// Examples:
//   "bold red"      -> "1;31"
//   "38;5;214"      -> "38;5;214" (raw sequence passthrough)
//   "color214"      -> "38;5;214"
//   "rgb(255,0,0)"  -> "38;2;255;0;0" (truecolor if terminal supports; accepted anyway)
//   "faint"         -> "2"
//   "gray"          -> "90"
//   "teal"          -> 256-color teal-ish
func parseStyleSequence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Raw numeric sequence passthrough
	if isRawSeq(s) {
		return s
	}

	parts := strings.Fields(s)
	var codes []string
	for _, p := range parts {
		p = strings.ToLower(p)
		switch p {
		case "bold":
			codes = append(codes, "1")
		case "faint", "dim":
			codes = append(codes, "2")
		case "italic":
			codes = append(codes, "3")
		case "underline", "ul":
			codes = append(codes, "4")
		case "blink":
			codes = append(codes, "5")
		case "reverse":
			codes = append(codes, "7")
		case "hidden":
			codes = append(codes, "8")
		case "strike", "strikethrough":
			codes = append(codes, "9")
		case "black":
			codes = append(codes, "30")
		case "red":
			codes = append(codes, "31")
		case "green":
			codes = append(codes, "32")
		case "yellow":
			codes = append(codes, "33")
		case "blue":
			codes = append(codes, "34")
		case "magenta":
			codes = append(codes, "35")
		case "cyan":
			codes = append(codes, "36")
		case "white":
			codes = append(codes, "37")
		case "gray", "grey":
			codes = append(codes, "90")
		case "bright-red":
			codes = append(codes, "91")
		case "bright-green":
			codes = append(codes, "92")
		case "bright-yellow":
			codes = append(codes, "93")
		case "bright-blue":
			codes = append(codes, "94")
		case "bright-magenta":
			codes = append(codes, "95")
		case "bright-cyan":
			codes = append(codes, "96")
		case "bright-white":
			codes = append(codes, "97")
		case "teal":
			codes = append(codes, "38;5;44")
		case "mauve":
			codes = append(codes, "38;5;183")
		case "lavender":
			codes = append(codes, "38;5;147")
		case "peach":
			codes = append(codes, "38;5;216")
		case "rose":
			codes = append(codes, "38;5;175")
		default:
			// color214 / colorNN -> 256-color FG
			if strings.HasPrefix(p, "color") {
				if n, err := strconv.Atoi(strings.TrimPrefix(p, "color")); err == nil {
					if n >= 0 && n <= 255 {
						codes = append(codes, fmt.Sprintf("38;5;%d", n))
						continue
					}
				}
			}
			// rgb(r,g,b)
			if strings.HasPrefix(p, "rgb(") && strings.HasSuffix(p, ")") {
				body := strings.TrimSuffix(strings.TrimPrefix(p, "rgb("), ")")
				nums := strings.Split(body, ",")
				if len(nums) == 3 {
					r, rErr := strconv.Atoi(strings.TrimSpace(nums[0]))
					g, gErr := strconv.Atoi(strings.TrimSpace(nums[1]))
					b, bErr := strconv.Atoi(strings.TrimSpace(nums[2]))
					if rErr == nil && gErr == nil && bErr == nil &&
						r >= 0 && r <= 255 && g >= 0 && g <= 255 && b >= 0 && b <= 255 {
						codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", r, g, b))
						continue
					}
				}
			}
			// Unknown token; ignore
		}
	}
	return strings.Join(codes, ";")
}

func isRawSeq(s string) bool {
	// allow "38;5;214", "1;31", etc.
	for _, r := range s {
		if (r < '0' || r > '9') && r != ';' {
			return false
		}
	}
	return strings.ContainsRune(s, ';') || isAllDigits(s)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func seq(code string) string { return code }

// ---------- Optional helpers for external integration ----------

// ResolveTheme is an alias for LoadTheme; provided to mirror other loader naming.
func ResolveTheme(explicitPath string) Theme { return LoadTheme(explicitPath) }

// SaveTheme writes a ThemeFile JSON to the default theme path (or explicit path).
// This is a convenience helper; pass an already-merged ThemeFile.
func SaveTheme(explicitPath string, tf ThemeFile) error {
	path := strings.TrimSpace(explicitPath)
	if path == "" {
		p, err := defaultThemePath()
		if err != nil {
			return err
		}
		path = p
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
