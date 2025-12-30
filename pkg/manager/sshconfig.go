package manager

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SSHHostEntry represents a single, literal Host alias parsed from an OpenSSH
// client configuration file (e.g., ~/.ssh/config). Wildcard host patterns are
// ignored for conversion purposes.
type SSHHostEntry struct {
	// Alias is the Host alias used on the ssh command line (e.g., "prod-db-1").
	Alias string

	// Patterns are the original patterns declared in the Host block.
	// When multiple patterns are present, one SSHHostEntry is produced per
	// literal (non-wildcard) pattern.
	Patterns []string

	// Values parsed from the Host block (last-wins semantics).
	HostName      string
	User          string
	Port          int
	ProxyJump     string
	ForwardAgent  *bool
	IdentityFiles []string

	// Source file path and starting line of the Host block (best-effort).
	Source    string
	StartLine int
}

// LoadSSHConfigDefault loads SSH config starting from ~/.ssh/config,
// processing simple Include directives (globs supported) at the top level.
// It returns one SSHHostEntry per literal Host alias (wildcards are skipped).
func LoadSSHConfigDefault() ([]SSHHostEntry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".ssh", "config")
	return LoadSSHConfig(path)
}

// LoadSSHConfig loads one or more SSH config files and returns literal Host
// alias entries. It processes simple top-level Include directives in each file.
// Later files and later Host blocks override earlier ones for the same alias.
func LoadSSHConfig(paths ...string) ([]SSHHostEntry, error) {
	if len(paths) == 0 {
		return nil, errors.New("no ssh config paths provided")
	}

	visited := map[string]struct{}{}
	allEntries := make([]SSHHostEntry, 0, 128)
	// alias -> index in allEntries; for last-wins replacement
	indexByAlias := map[string]int{}

	for _, p := range paths {
		p = expandUserAndEnv(p)
		entries, err := parseSSHConfigRecursive(p, visited)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if prev, ok := indexByAlias[e.Alias]; ok {
				allEntries[prev] = e
			} else {
				indexByAlias[e.Alias] = len(allEntries)
				allEntries = append(allEntries, e)
			}
		}
	}

	// Sort by alias for stable output
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].Alias < allEntries[j].Alias
	})

	return allEntries, nil
}

// ConvertSSHToConfig converts parsed SSH Host entries into the internal Config.
// - One Host per SSH alias
// - user/port/jump_host are mapped from User/Port/ProxyJump
// - Host.Name is set to the alias (so "ssh <alias>" works and leverages ssh config)
// - Tags include "sshconfig" and, if HostName differs from alias, a tag noting it
func ConvertSSHToConfig(entries []SSHHostEntry) *Config {
	out := &Config{
		Groups: nil,
		Hosts:  make([]Host, 0, len(entries)),
	}
	for _, e := range entries {
		h := Host{
			Name:     e.Alias,
			User:     e.User,
			Port:     e.Port,
			JumpHost: normalizeProxyJump(e.ProxyJump),
			Tags:     []string{"sshconfig"},
		}
		// If the HostName differs and is useful to display, add as a tag.
		if e.HostName != "" && e.HostName != e.Alias {
			h.Tags = append(h.Tags, "hostname:"+e.HostName)
		}
		out.Hosts = append(out.Hosts, h)
	}
	return out
}

// LoadConfigFromSSH is a convenience that loads SSH config from the provided
// paths (or default if none), and returns an internal Config representation.
func LoadConfigFromSSH(paths ...string) (*Config, error) {
	var entries []SSHHostEntry
	var err error
	if len(paths) == 0 {
		entries, err = LoadSSHConfigDefault()
	} else {
		entries, err = LoadSSHConfig(paths...)
	}
	if err != nil {
		return nil, err
	}
	return ConvertSSHToConfig(entries), nil
}

// --------------------
// Parsing internals
// --------------------

type hostBlock struct {
	patterns  []string
	settings  map[string][]string // key -> all values (we use last for most keys)
	source    string
	startLine int
}

func newHostBlock(source string, startLine int) *hostBlock {
	return &hostBlock{
		patterns:  nil,
		settings:  map[string][]string{},
		source:    source,
		startLine: startLine,
	}
}

func (hb *hostBlock) set(key, value string) {
	k := strings.ToLower(strings.TrimSpace(key))
	v := strings.TrimSpace(value)
	if k == "" {
		return
	}
	// Multi-value accumulation for identityfile; last-wins for others.
	switch k {
	case "identityfile":
		hb.settings[k] = append(hb.settings[k], v)
	default:
		hb.settings[k] = []string{v}
	}
}

func (hb *hostBlock) toEntries() []SSHHostEntry {
	if len(hb.patterns) == 0 {
		return nil
	}

	// Extract last values for common keys
	getLast := func(k string) string {
		if vals, ok := hb.settings[k]; ok && len(vals) > 0 {
			return vals[len(vals)-1]
		}
		return ""
	}
	getBoolPtr := func(k string) *bool {
		if vals, ok := hb.settings[k]; ok && len(vals) > 0 {
			if b, ok := parseSSHBool(vals[len(vals)-1]); ok {
				return &b
			}
		}
		return nil
	}

	var idFiles []string
	if v, ok := hb.settings["identityfile"]; ok {
		idFiles = append(idFiles, v...)
	}

	hostName := getLast("hostname")
	user := getLast("user")
	portStr := getLast("port")
	proxyJump := getLast("proxyjump")
	forwardAgent := getBoolPtr("forwardagent")

	port := 0
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			port = p
		}
	}

	entries := make([]SSHHostEntry, 0, len(hb.patterns))
	for _, pat := range hb.patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" || !isLiteralHostPattern(pat) {
			// Skip wildcard/negated patterns for conversion purposes
			continue
		}
		entries = append(entries, SSHHostEntry{
			Alias:         pat,
			Patterns:      append([]string(nil), hb.patterns...),
			HostName:      hostName,
			User:          user,
			Port:          port,
			ProxyJump:     proxyJump,
			ForwardAgent:  forwardAgent,
			IdentityFiles: append([]string(nil), idFiles...),
			Source:        hb.source,
			StartLine:     hb.startLine,
		})
	}
	return entries
}

func parseSSHConfigRecursive(path string, visited map[string]struct{}) ([]SSHHostEntry, error) {
	var out []SSHHostEntry

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if _, ok := visited[abs]; ok {
		return out, nil
	}
	visited[abs] = struct{}{}

	f, err := os.Open(abs)
	if err != nil {
		// It's common for some Include globs to not match; ignore missing files.
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("open ssh config %s: %w", abs, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var current *hostBlock
	lineNo := 0

	flush := func() {
		if current != nil {
			out = append(out, current.toEntries()...)
			current = nil
		}
	}

	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(stripSSHInlineComment(sc.Text()))

		if line == "" {
			continue
		}

		// Split "Key [Value...]" form
		// Keys and values can be separated by whitespace or '='
		key, val, hasKey := splitKeyVal(line)
		if !hasKey {
			continue
		}

		lkey := strings.ToLower(key)
		switch lkey {
		case "host":
			// New block; flush previous
			flush()
			current = newHostBlock(abs, lineNo)
			// Patterns can be space-separated
			patterns := strings.Fields(val)
			current.patterns = append(current.patterns, patterns...)
		case "include":
			// Process includes at top level; if inside a block, we still process
			// them "in place" by flushing the current block so order is preserved.
			flush()
			expanded := expandIncludePatterns(abs, val)
			for _, inc := range expanded {
				children, err := parseSSHConfigRecursive(inc, visited)
				if err != nil {
					return nil, err
				}
				out = append(out, children...)
			}
		case "match":
			// We don't evaluate Match conditions; ignore the entire Match section
			// by flushing the current block and skipping until next Host/EOF.
			flush()
			// Skip lines until next Host or EOF
			// We'll keep it simple: do nothing special here; the loop continues
			// and we'll essentially ignore settings that follow (since not in a Host block).
			continue
		default:
			// Regular key inside a Host block
			if current == nil {
				// Top-level setting outside any Host; ignore for now.
				continue
			}
			current.set(lkey, val)
		}
	}
	// Flush last block
	flush()

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan ssh config %s: %w", abs, err)
	}

	return out, nil
}

func stripSSHInlineComment(s string) string {
	// Remove comments starting with '#' unless inside simple quotes or double quotes.
	// This is a best-effort heuristic sufficient for common ssh config usage.
	var out strings.Builder
	inSingle := false
	inDouble := false
	for i, r := range s {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return strings.TrimRightFunc(out.String(), func(r rune) bool { return r == ' ' || r == '\t' })
			}
		}
		out.WriteRune(r)
		// handle CRLF by ignoring; Scanner already strips
		if i == len(s)-1 {
			return out.String()
		}
	}
	return out.String()
}

func splitKeyVal(line string) (key, val string, ok bool) {
	// Accept forms: "Key Value", "Key=Value", key case-insensitive.
	// Leading/trailing spaces trimmed previously.
	if line == "" {
		return "", "", false
	}
	// Use first whitespace or '=' as delimiter
	if i := strings.IndexAny(line, " \t="); i >= 0 {
		key = strings.TrimSpace(line[:i])
		val = strings.TrimSpace(line[i+1:])
		if key == "" {
			return "", "", false
		}
		return key, val, true
	}
	// Key with no value (rare); treat as not ok
	return "", "", false
}

func expandIncludePatterns(baseFile, pattern string) []string {
	pattern = expandUserAndEnv(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil
	}
	// If pattern is not absolute, make it relative to the directory of baseFile
	if !filepath.IsAbs(pattern) {
		baseDir := filepath.Dir(baseFile)
		pattern = filepath.Join(baseDir, pattern)
	}
	// Support globs
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		// Only files
		fi, err := os.Stat(m)
		if err == nil && !fi.IsDir() {
			out = append(out, m)
		}
	}
	return out
}

func expandUserAndEnv(p string) string {
	if p == "" {
		return ""
	}
	p = os.ExpandEnv(p)
	if strings.HasPrefix(p, "~") {
		if p == "~" {
			if h, _ := os.UserHomeDir(); h != "" {
				return h
			}
			return p
		}
		if strings.HasPrefix(p, "~/") {
			if h, _ := os.UserHomeDir(); h != "" {
				return filepath.Join(h, p[2:])
			}
		}
	}
	return p
}

func isLiteralHostPattern(p string) bool {
	// OpenSSH supports patterns with '*', '?', '[]' and negation with '!'.
	// We'll consider it literal if none of those pattern metacharacters are present
	// and it doesn't start with '!'.
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "!") {
		return false
	}
	if strings.ContainsAny(p, "*?[]") {
		return false
	}
	// Also exclude patterns that contain whitespace (invalid alias)
	if strings.IndexFunc(p, func(r rune) bool { return r == ' ' || r == '\t' }) >= 0 {
		return false
	}
	return true
}

func parseSSHBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "true", "on", "1":
		return true, true
	case "no", "false", "off", "0":
		return false, true
	default:
		return false, false
	}
}

// normalizeProxyJump extracts the first hop of ProxyJump, ignoring any port
// specification, and returns "[user@]host" if present. Multiple hops are
// comma-separated in ProxyJump; internal Host supports a single jump host.
func normalizeProxyJump(pj string) string {
	pj = strings.TrimSpace(pj)
	if pj == "" {
		return ""
	}
	// First hop only
	if i := strings.IndexByte(pj, ','); i >= 0 {
		pj = pj[:i]
	}
	pj = strings.TrimSpace(pj)
	// Remove trailing ":port" if present (ssh's ProxyJump allows :port)
	if colon := strings.LastIndexByte(pj, ':'); colon >= 0 {
		// Only strip if there is no ']' indicating IPv6 literal or we are sure it's a port.
		// For simplicity, if there's '@' earlier, and after colon it's all digits, strip it.
		if colon > strings.LastIndexByte(pj, '@') {
			if _, err := strconv.Atoi(pj[colon+1:]); err == nil {
				pj = pj[:colon]
			}
		}
	}
	return pj
}
