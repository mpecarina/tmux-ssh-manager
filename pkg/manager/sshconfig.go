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

// SSHConfigBlock is a structured representation of a single Host block as it
// appears in an SSH client config file.
//
// This exists to support in-place editing of the primary SSH config (~/.ssh/config)
// while preserving formatting and comments as much as possible.
type SSHConfigBlock struct {
	// Source is the file path this block came from (absolute path where possible).
	Source string

	// StartLine/EndLine are 1-based, inclusive line numbers in Source.
	StartLine int
	EndLine   int

	// RawLines are the original lines of the block, including comments and blank lines,
	// as read from disk. They are not trimmed.
	RawLines []string

	// Patterns are the original patterns declared on the Host line (space-separated).
	Patterns []string

	// Settings are parsed key/value pairs from inside the block.
	// Keys are lowercased. Values preserve original token trimming rules.
	// For most keys, last-wins semantics apply; for IdentityFile, values accumulate.
	Settings map[string][]string
}

// SSHConfigFile is the structured parse result for a single ssh config file.
type SSHConfigFile struct {
	Path   string
	Lines  []string
	Blocks []SSHConfigBlock
}

// SSHPrimaryDedupeReport reports duplicates for literal aliases within the
// primary SSH config (~/.ssh/config) only.
type SSHPrimaryDedupeReport struct {
	// AliasToBlocks maps a literal alias to all matching Host blocks (in file order).
	// Only aliases with >=1 occurrences are included; duplicates are those with len>1.
	AliasToBlocks map[string][]SSHConfigBlock
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

// LoadSSHConfigPrimaryPath returns the canonical primary OpenSSH client config
// path used for direct editing: ~/.ssh/config.
func LoadSSHConfigPrimaryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// LoadSSHConfigPrimaryStructured parses ONLY the primary SSH config (~/.ssh/config)
// and returns a structured representation suitable for in-place editing.
//
// Notes:
// - This does not evaluate Match conditions.
// - Include directives are ignored for editing purposes (primary file only).
// - Host blocks are identified by "Host ..." directives; content is preserved as raw lines.
func LoadSSHConfigPrimaryStructured() (*SSHConfigFile, error) {
	p, err := LoadSSHConfigPrimaryPath()
	if err != nil {
		return nil, err
	}
	p = expandUserAndEnv(p)

	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}

	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("open ssh config %s: %w", abs, err)
	}
	defer f.Close()

	lines := make([]string, 0, 1024)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan ssh config %s: %w", abs, err)
	}

	sf := &SSHConfigFile{
		Path:   abs,
		Lines:  lines,
		Blocks: nil,
	}

	// Identify Host blocks by scanning linearly. We preserve raw lines exactly.
	var curStart int = -1 // 0-based index into Lines
	for i := 0; i < len(lines); i++ {
		trim := strings.TrimSpace(stripSSHInlineComment(lines[i]))
		if trim == "" {
			continue
		}
		k, val, ok := splitKeyVal(trim)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "host") {
			// Flush previous block
			if curStart >= 0 {
				sf.Blocks = append(sf.Blocks, buildStructuredHostBlock(abs, curStart, i-1, lines[curStart:i]))
			}
			curStart = i
			_ = val
		}
		// We intentionally do not start blocks on Match (ignored for editing).
	}
	// Flush last block to EOF
	if curStart >= 0 && curStart < len(lines) {
		sf.Blocks = append(sf.Blocks, buildStructuredHostBlock(abs, curStart, len(lines)-1, lines[curStart:]))
	}

	return sf, nil
}

// ComputePrimaryDuplicateAliases returns a report of duplicate literal aliases
// within the primary ssh config (~/.ssh/config) only.
func ComputePrimaryDuplicateAliases() (*SSHPrimaryDedupeReport, error) {
	sf, err := LoadSSHConfigPrimaryStructured()
	if err != nil {
		return nil, err
	}
	m := make(map[string][]SSHConfigBlock, 64)
	for _, b := range sf.Blocks {
		for _, pat := range b.Patterns {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			if !isLiteralHostPattern(pat) {
				continue
			}
			m[pat] = append(m[pat], b)
		}
	}
	return &SSHPrimaryDedupeReport{AliasToBlocks: m}, nil
}

// MergePrimaryDuplicateAlias merges duplicate literal alias blocks inside the
// primary ssh config (~/.ssh/config) by:
// - selecting the FIRST occurrence block as the target to keep
// - replacing its body with a merged block (last-wins per key; IdentityFile accumulates)
// - deleting all other occurrences (entire block spans)
//
// It returns (changed, error). If alias has <2 occurrences, changed=false.
func MergePrimaryDuplicateAlias(alias string) (bool, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return false, errors.New("merge primary ssh alias: empty alias")
	}

	sf, err := LoadSSHConfigPrimaryStructured()
	if err != nil {
		return false, err
	}

	// Find blocks containing this alias as a literal pattern.
	indices := make([]int, 0, 4)
	for i := range sf.Blocks {
		for _, pat := range sf.Blocks[i].Patterns {
			if strings.TrimSpace(pat) == alias && isLiteralHostPattern(alias) {
				indices = append(indices, i)
				break
			}
		}
	}
	if len(indices) < 2 {
		return false, nil
	}

	// Merge settings in file order (later blocks override earlier for last-wins keys).
	merged := make(map[string][]string, 16)
	mergedIdentity := make([]string, 0, 8)

	for _, bi := range indices {
		b := sf.Blocks[bi]
		for k, vals := range b.Settings {
			lk := strings.ToLower(strings.TrimSpace(k))
			if lk == "" {
				continue
			}
			switch lk {
			case "identityfile":
				for _, v := range vals {
					v = strings.TrimSpace(v)
					if v != "" {
						mergedIdentity = append(mergedIdentity, v)
					}
				}
			default:
				// last-wins: store only the last value slice (typically len==1)
				if len(vals) > 0 {
					merged[lk] = []string{strings.TrimSpace(vals[len(vals)-1])}
				}
			}
		}
	}
	if len(mergedIdentity) > 0 {
		merged["identityfile"] = append([]string(nil), mergedIdentity...)
	}

	// Choose first occurrence to keep.
	keep := sf.Blocks[indices[0]]

	// Render a new block using the first block's Host patterns (preserve as-is).
	indent := detectSSHIndent(keep.RawLines)
	newBlockLines := renderSSHHostBlockLines(keep.Patterns, merged, indent)

	// Apply patch: replace keep block span with new block, delete other spans.
	//
	// We operate on sf.Lines (original file lines) using 0-based indices.
	// Spans are inclusive based on StartLine/EndLine (1-based).
	type span struct {
		start int // 0-based inclusive
		end   int // 0-based inclusive
		keep  bool
	}
	spans := make([]span, 0, len(indices))
	for j, bi := range indices {
		b := sf.Blocks[bi]
		s := span{start: b.StartLine - 1, end: b.EndLine - 1, keep: j == 0}
		spans = append(spans, s)
	}
	// Sort spans by start ascending so we can apply from bottom up.
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	// Apply bottom-up to avoid index shifts.
	out := append([]string(nil), sf.Lines...)
	for i := len(spans) - 1; i >= 0; i-- {
		s := spans[i]
		if s.start < 0 || s.end < s.start || s.end >= len(out) {
			return false, fmt.Errorf("merge primary ssh alias: invalid span %d..%d", s.start, s.end)
		}
		if s.keep {
			// replace range with newBlockLines
			out = spliceLines(out, s.start, s.end, newBlockLines)
		} else {
			// delete range entirely
			out = spliceLines(out, s.start, s.end, nil)
		}
	}

	// Write back atomically with backup.
	if err := writeSSHConfigAtomicWithBackup(sf.Path, out); err != nil {
		return false, err
	}
	return true, nil
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

// buildStructuredHostBlock constructs an SSHConfigBlock from a span of lines.
// startIdx/endIdx are 0-based indices into the file (inclusive).
func buildStructuredHostBlock(source string, startIdx int, endIdx int, raw []string) SSHConfigBlock {
	b := SSHConfigBlock{
		Source:    source,
		StartLine: startIdx + 1,
		EndLine:   endIdx + 1,
		RawLines:  append([]string(nil), raw...),
		Patterns:  nil,
		Settings:  map[string][]string{},
	}

	// Parse the Host line patterns (best-effort).
	if len(raw) > 0 {
		trim := strings.TrimSpace(stripSSHInlineComment(raw[0]))
		k, val, ok := splitKeyVal(trim)
		if ok && strings.EqualFold(strings.TrimSpace(k), "host") {
			b.Patterns = append(b.Patterns, strings.Fields(val)...)
		}
	}

	// Parse inner settings (best-effort), preserving identityfile accumulation.
	for i := 1; i < len(raw); i++ {
		line := strings.TrimSpace(stripSSHInlineComment(raw[i]))
		if line == "" {
			continue
		}
		key, val, ok := splitKeyVal(line)
		if !ok {
			continue
		}
		lkey := strings.ToLower(strings.TrimSpace(key))
		if lkey == "" {
			continue
		}
		switch lkey {
		case "host":
			// Defensive: shouldn't happen in the middle of a block span.
			continue
		case "match":
			// We ignore Match conditions for editing/merging.
			continue
		default:
			// multi-value accumulation for identityfile
			if lkey == "identityfile" {
				b.Settings[lkey] = append(b.Settings[lkey], strings.TrimSpace(val))
			} else {
				b.Settings[lkey] = []string{strings.TrimSpace(val)}
			}
		}
	}

	return b
}

func detectSSHIndent(rawLines []string) string {
	// Heuristic: find first non-empty, non-comment setting line and return its leading whitespace.
	for i := 1; i < len(rawLines); i++ {
		ln := rawLines[i]
		if strings.TrimSpace(ln) == "" {
			continue
		}
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			continue
		}
		// leading whitespace
		j := 0
		for j < len(ln) {
			if ln[j] != ' ' && ln[j] != '\t' {
				break
			}
			j++
		}
		if j > 0 {
			return ln[:j]
		}
	}
	// Default to two spaces (matches common ssh config style).
	return "  "
}

func renderSSHHostBlockLines(patterns []string, settings map[string][]string, indent string) []string {
	// Render Host line first.
	pats := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p != "" {
			pats = append(pats, p)
		}
	}
	hostLine := "Host " + strings.Join(pats, " ")
	out := []string{hostLine}

	// Stable key ordering for deterministic output.
	keys := make([]string, 0, len(settings))
	for k := range settings {
		kk := strings.ToLower(strings.TrimSpace(k))
		if kk == "" {
			continue
		}
		keys = append(keys, kk)
	}
	sort.Strings(keys)

	seen := map[string]struct{}{}
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		vals := settings[k]
		if len(vals) == 0 {
			continue
		}
		// IdentityFile can be multi-line.
		if k == "identityfile" {
			for _, v := range vals {
				v = strings.TrimSpace(v)
				if v != "" {
					out = append(out, indent+"IdentityFile "+v)
				}
			}
			continue
		}
		v := strings.TrimSpace(vals[len(vals)-1])
		if v == "" {
			continue
		}
		// Preserve canonical-ish casing for common keys.
		keyCased := k
		switch k {
		case "hostname":
			keyCased = "HostName"
		case "proxyjump":
			keyCased = "ProxyJump"
		case "forwardagent":
			keyCased = "ForwardAgent"
		case "identityfile":
			keyCased = "IdentityFile"
		case "user":
			keyCased = "User"
		case "port":
			keyCased = "Port"
		default:
			// Title-case first letter as a mild readability improvement.
			keyCased = strings.ToUpper(k[:1]) + k[1:]
		}
		out = append(out, indent+keyCased+" "+v)
	}

	return out
}

// spliceLines replaces the inclusive [start,end] span with replacement (which may be nil).
func spliceLines(lines []string, start int, end int, replacement []string) []string {
	if start < 0 {
		start = 0
	}
	if end >= len(lines) {
		end = len(lines) - 1
	}
	if start > end || len(lines) == 0 {
		// Degenerate: append replacement
		return append(append([]string(nil), lines...), replacement...)
	}
	out := make([]string, 0, len(lines)-(end-start+1)+len(replacement))
	out = append(out, lines[:start]...)
	if len(replacement) > 0 {
		out = append(out, replacement...)
	}
	if end+1 < len(lines) {
		out = append(out, lines[end+1:]...)
	}
	return out
}

func writeSSHConfigAtomicWithBackup(path string, lines []string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("write ssh config: empty path")
	}

	// Ensure parent dir exists.
	dir := filepath.Dir(path)
	if dir == "" {
		return fmt.Errorf("write ssh config: invalid dir for %q", path)
	}

	// Backup best-effort.
	// We keep it simple: create a sibling file with .bak suffix (timestamp-free here to avoid time import).
	// If it already exists, we overwrite it (last backup wins).
	backupPath := path + ".bak"
	if data, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(backupPath, data, 0o600)
	}

	tmp := path + ".tmp"
	payload := strings.Join(lines, "\n")
	// Preserve trailing newline if file had one originally or if non-empty.
	if len(lines) > 0 && !strings.HasSuffix(payload, "\n") {
		payload += "\n"
	}
	if err := os.WriteFile(tmp, []byte(payload), 0o600); err != nil {
		return fmt.Errorf("write ssh config tmp %s: %w", tmp, err)
	}

	// Atomic replace (best-effort on macOS).
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace ssh config %s: %w", path, err)
	}
	return nil
}

// ExportSSHConfigSelected renders and writes an ssh config file containing only the selected literal Host aliases.
// - `entries` should typically come from LoadSSHConfigDefault/LoadSSHConfigPrimaryStructured.
// - `selectedAliases` is a set of Host aliases to include (exact match).
// - `outPath` is where to write the new config (written atomically; a .bak is created/overwritten if file exists).
func ExportSSHConfigSelected(entries []SSHHostEntry, selectedAliases map[string]struct{}, outPath string) (int, error) {
	if strings.TrimSpace(outPath) == "" {
		return 0, errors.New("ssh export: outPath is required")
	}
	if len(selectedAliases) == 0 {
		return 0, errors.New("ssh export: no hosts selected")
	}

	// Build per-alias settings. We choose a deterministic and minimal output:
	// Host, then (HostName/User/Port/ProxyJump/ForwardAgent/IdentityFile... when present).
	// If an alias appears multiple times in `entries`, later entries win for single-valued keys;
	// IdentityFiles are accumulated uniquely (preserving first-seen order).
	type agg struct {
		hostName      string
		user          string
		port          int
		proxyJump     string
		forwardAgent  *bool
		identityFiles []string
	}
	byAlias := map[string]*agg{}

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

	for _, e := range entries {
		alias := strings.TrimSpace(e.Alias)
		if alias == "" {
			continue
		}
		if _, ok := selectedAliases[alias]; !ok {
			continue
		}

		a := byAlias[alias]
		if a == nil {
			a = &agg{}
			byAlias[alias] = a
		}

		// last-wins for scalars when non-empty
		if strings.TrimSpace(e.HostName) != "" {
			a.hostName = strings.TrimSpace(e.HostName)
		}
		if strings.TrimSpace(e.User) != "" {
			a.user = strings.TrimSpace(e.User)
		}
		if e.Port > 0 {
			a.port = e.Port
		}
		if strings.TrimSpace(e.ProxyJump) != "" {
			a.proxyJump = strings.TrimSpace(e.ProxyJump)
		}
		if e.ForwardAgent != nil {
			b := *e.ForwardAgent
			a.forwardAgent = &b
		}
		for _, id := range e.IdentityFiles {
			a.identityFiles = addUnique(a.identityFiles, id)
		}
	}

	// Deterministic order: sort aliases.
	aliases := make([]string, 0, len(byAlias))
	for alias := range byAlias {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	// Render.
	indent := "  "
	lines := []string{
		"# Generated by tmux-ssh-manager (ssh export)",
		"# Source: ~/.ssh/config (+ Includes if present)",
		"#",
	}
	written := 0
	for _, alias := range aliases {
		a := byAlias[alias]
		if a == nil {
			continue
		}

		settings := map[string][]string{}

		// HostName: write if known. If missing, default to alias (keeps output usable).
		hn := strings.TrimSpace(a.hostName)
		if hn == "" {
			hn = alias
		}
		settings["hostname"] = []string{hn}

		if u := strings.TrimSpace(a.user); u != "" {
			settings["user"] = []string{u}
		}
		if a.port > 0 {
			settings["port"] = []string{strconv.Itoa(a.port)}
		}
		if pj := strings.TrimSpace(a.proxyJump); pj != "" {
			settings["proxyjump"] = []string{pj}
		}
		if a.forwardAgent != nil {
			if *a.forwardAgent {
				settings["forwardagent"] = []string{"yes"}
			} else {
				settings["forwardagent"] = []string{"no"}
			}
		}
		if len(a.identityFiles) > 0 {
			// Preserve multiple IdentityFile lines.
			vals := make([]string, 0, len(a.identityFiles))
			for _, id := range a.identityFiles {
				id = strings.TrimSpace(id)
				if id != "" {
					vals = append(vals, id)
				}
			}
			if len(vals) > 0 {
				settings["identityfile"] = vals
			}
		}

		if written > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, renderSSHHostBlockLines([]string{alias}, settings, indent)...)
		written++
	}

	// Keep a trailing blank line for nice future appends.
	lines = append(lines, "")

	if written == 0 {
		return 0, errors.New("ssh export: none of the selected aliases were found in parsed entries")
	}

	abs := expandUserAndEnv(outPath)
	if p, err := filepath.Abs(abs); err == nil {
		abs = p
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return 0, fmt.Errorf("ssh export: create output dir: %w", err)
	}

	if err := writeSSHConfigAtomicWithBackup(abs, lines); err != nil {
		return 0, fmt.Errorf("ssh export: write %s: %w", abs, err)
	}
	return written, nil
}

// AddPrimaryHostParams describes a new Host block to append to ~/.ssh/config.
type AddPrimaryHostParams struct {
	Alias        string
	HostName     string
	User         string
	Port         int
	ProxyJump    string
	ForwardAgent bool // default true (caller can set true explicitly)
	IdentityFile string
}

// AppendPrimaryHostBlock appends a new Host block to the primary ssh config (~/.ssh/config).
//
// Behavior:
// - Always writes HostName (defaults to Alias if HostName is blank) so it is visible/searchable in ssh -G output.
// - Defaults ForwardAgent to "yes" when ForwardAgent is true (recommended default for your workflow).
// - Creates ~/.ssh/config if it does not exist.
// - Writes atomically and creates/overwrites ~/.ssh/config.bak as a backup.
func AppendPrimaryHostBlock(p AddPrimaryHostParams) error {
	alias := strings.TrimSpace(p.Alias)
	if alias == "" {
		return errors.New("append ssh host: alias is required")
	}
	if !isLiteralHostPattern(alias) {
		return fmt.Errorf("append ssh host: alias must be a literal Host pattern (got %q)", alias)
	}

	primary, err := LoadSSHConfigPrimaryPath()
	if err != nil {
		return err
	}
	primary = expandUserAndEnv(primary)

	abs, err := filepath.Abs(primary)
	if err != nil {
		abs = primary
	}

	// Read existing file if present; otherwise start empty.
	var lines []string
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		if !os.IsNotExist(rerr) {
			return fmt.Errorf("append ssh host: read %s: %w", abs, rerr)
		}
		lines = []string{}
	} else {
		// Normalize to \n (ssh config is line-oriented; we preserve content as lines).
		txt := strings.ReplaceAll(string(data), "\r\n", "\n")
		txt = strings.ReplaceAll(txt, "\r", "\n")
		// Split and drop final empty line if the file ends with newline so we can control spacing.
		parts := strings.Split(txt, "\n")
		if len(parts) > 0 && parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
		lines = parts
	}

	// Build settings.
	settings := map[string][]string{}

	// Always write HostName, defaulting to alias.
	hostName := strings.TrimSpace(p.HostName)
	if hostName == "" {
		hostName = alias
	}
	settings["hostname"] = []string{hostName}

	if u := strings.TrimSpace(p.User); u != "" {
		settings["user"] = []string{u}
	}
	if p.Port > 0 {
		settings["port"] = []string{strconv.Itoa(p.Port)}
	}
	if pj := strings.TrimSpace(p.ProxyJump); pj != "" {
		settings["proxyjump"] = []string{pj}
	}

	// Default ForwardAgent to yes when enabled.
	// (Caller should pass ForwardAgent=true to match your desired default.)
	if p.ForwardAgent {
		settings["forwardagent"] = []string{"yes"}
	}

	if id := strings.TrimSpace(p.IdentityFile); id != "" {
		settings["identityfile"] = []string{id}
	}

	newBlock := renderSSHHostBlockLines([]string{alias}, settings, "  ")

	// Ensure a clean separation: add a blank line before the new block if file isn't empty
	// and the last line isn't already blank.
	if len(lines) > 0 {
		if strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
	}

	lines = append(lines, newBlock...)

	// Ensure file ends with a blank line (nice UX; ssh doesn't care, but it keeps future appends clean).
	lines = append(lines, "")

	if err := writeSSHConfigAtomicWithBackup(abs, lines); err != nil {
		return err
	}
	return nil
}
