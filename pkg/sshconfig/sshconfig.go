package sshconfig

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Host struct {
	Alias         string
	HostName      string
	User          string
	Port          int
	ProxyJump     string
	IdentityFiles []string
	SourcePath    string
	SourceLine    int
}

type AddHostInput struct {
	Alias        string
	HostName     string
	User         string
	Port         int
	ProxyJump    string
	IdentityFile string
}

func DefaultPrimaryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

func LoadDefault() ([]Host, error) {
	path, err := DefaultPrimaryPath()
	if err != nil {
		return nil, err
	}
	return Load(path)
}

func Load(paths ...string) ([]Host, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no ssh config paths provided")
	}

	visited := map[string]struct{}{}
	merged := map[string]Host{}
	order := make([]string, 0, 64)

	for _, path := range paths {
		path = expandPath(path)
		entries, err := parseRecursive(path, visited)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if _, ok := merged[entry.Alias]; !ok {
				order = append(order, entry.Alias)
			}
			merged[entry.Alias] = entry
		}
	}

	out := make([]Host, 0, len(merged))
	for _, alias := range order {
		if host, ok := merged[alias]; ok {
			out = append(out, host)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Alias < out[j].Alias
	})
	return out, nil
}

func AddHostToPrimary(input AddHostInput) error {
	path, err := DefaultPrimaryPath()
	if err != nil {
		return err
	}
	return AddHost(path, input)
}

func AddHost(path string, input AddHostInput) error {
	input.Alias = strings.TrimSpace(input.Alias)
	input.HostName = strings.TrimSpace(input.HostName)
	input.User = strings.TrimSpace(input.User)
	input.ProxyJump = strings.TrimSpace(input.ProxyJump)
	input.IdentityFile = strings.TrimSpace(input.IdentityFile)

	if input.Alias == "" {
		return fmt.Errorf("alias is required")
	}
	if input.HostName == "" {
		input.HostName = input.Alias
	}

	current, err := Load(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, host := range current {
		if host.Alias == input.Alias {
			return fmt.Errorf("host alias already exists: %s", input.Alias)
		}
	}

	path = expandPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create ssh config dir: %w", err)
	}

	var builder strings.Builder
	if data, err := os.ReadFile(path); err == nil {
		builder.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			builder.WriteByte('\n')
		}
		builder.WriteByte('\n')
	}

	builder.WriteString("Host ")
	builder.WriteString(input.Alias)
	builder.WriteByte('\n')
	builder.WriteString("  HostName ")
	builder.WriteString(input.HostName)
	builder.WriteByte('\n')
	if input.User != "" {
		builder.WriteString("  User ")
		builder.WriteString(input.User)
		builder.WriteByte('\n')
	}
	if input.Port > 0 {
		builder.WriteString("  Port ")
		builder.WriteString(strconv.Itoa(input.Port))
		builder.WriteByte('\n')
	}
	if input.ProxyJump != "" {
		builder.WriteString("  ProxyJump ")
		builder.WriteString(input.ProxyJump)
		builder.WriteByte('\n')
	}
	if input.IdentityFile != "" {
		builder.WriteString("  IdentityFile ")
		builder.WriteString(input.IdentityFile)
		builder.WriteByte('\n')
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("write ssh config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace ssh config: %w", err)
	}
	return nil
}

type hostBlock struct {
	patterns  []string
	settings  map[string][]string
	source    string
	startLine int
}

func parseRecursive(path string, visited map[string]struct{}) ([]Host, error) {
	path = expandPath(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if _, ok := visited[abs]; ok {
		return nil, nil
	}
	visited[abs] = struct{}{}

	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open ssh config %s: %w", abs, err)
	}
	defer f.Close()

	var out []Host
	var current *hostBlock
	lineNo := 0

	flush := func() {
		if current == nil {
			return
		}
		out = append(out, current.toHosts()...)
		current = nil
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(stripInlineComment(scanner.Text()))
		if line == "" {
			continue
		}

		key, value, ok := splitDirective(line)
		if !ok {
			continue
		}

		switch strings.ToLower(key) {
		case "host":
			flush()
			current = &hostBlock{
				patterns:  strings.Fields(value),
				settings:  map[string][]string{},
				source:    abs,
				startLine: lineNo,
			}
		case "include":
			flush()
			for _, includePath := range expandIncludes(abs, value) {
				entries, err := parseRecursive(includePath, visited)
				if err != nil {
					return nil, err
				}
				out = append(out, entries...)
			}
		case "match":
			flush()
		default:
			if current == nil {
				continue
			}
			lkey := strings.ToLower(strings.TrimSpace(key))
			if lkey == "identityfile" {
				current.settings[lkey] = append(current.settings[lkey], strings.TrimSpace(value))
			} else {
				current.settings[lkey] = []string{strings.TrimSpace(value)}
			}
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan ssh config %s: %w", abs, err)
	}
	return out, nil
}

func (b *hostBlock) toHosts() []Host {
	if b == nil {
		return nil
	}
	var hosts []Host
	for _, pattern := range b.patterns {
		pattern = strings.TrimSpace(pattern)
		if !isLiteralPattern(pattern) {
			continue
		}
		hosts = append(hosts, Host{
			Alias:         pattern,
			HostName:      b.last("hostname"),
			User:          b.last("user"),
			Port:          parsePort(b.last("port")),
			ProxyJump:     b.last("proxyjump"),
			IdentityFiles: append([]string(nil), b.settings["identityfile"]...),
			SourcePath:    b.source,
			SourceLine:    b.startLine,
		})
	}
	return hosts
}

func (b *hostBlock) last(key string) string {
	values := b.settings[key]
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func parsePort(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 {
		return 0
	}
	return port
}

func expandIncludes(baseFile, raw string) []string {
	raw = expandPath(strings.TrimSpace(raw))
	if raw == "" {
		return nil
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(filepath.Dir(baseFile), raw)
	}
	matches, err := filepath.Glob(raw)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}

func stripInlineComment(line string) string {
	var builder strings.Builder
	inSingle := false
	inDouble := false
	for _, r := range line {
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
				return strings.TrimRight(builder.String(), " \t")
			}
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func splitDirective(line string) (string, string, bool) {
	if line == "" {
		return "", "", false
	}
	index := strings.IndexAny(line, " \t=")
	if index < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:index])
	value := strings.TrimSpace(line[index+1:])
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

func isLiteralPattern(pattern string) bool {
	if pattern == "" || strings.HasPrefix(pattern, "!") {
		return false
	}
	return !strings.ContainsAny(pattern, "*?[]")
}

func expandPath(path string) string {
	path = os.ExpandEnv(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
