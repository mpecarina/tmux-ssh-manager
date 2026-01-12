package manager

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file provides per-host daily log path helpers.
//
// Design goals:
// - Logs live under ~/.config/tmux-ssh-manager/logs/<hostkey>/YYYY-MM-DD.log by default
//   (or $XDG_CONFIG_HOME when set).
// - A new file is created per calendar day (local time by default) and appended to.
// - Helpers are conservative: no secrets are logged by default; callers decide what to write.
// - Host key is sanitized to be filesystem-safe.
// - Paths are deterministic and easy to navigate in the UI.
//
// NOTE: This file only provides filesystem helpers and small IO utilities.
// The TUI integration (e.g., viewing logs with vim-like shortcuts) should be implemented
// in the Bubble Tea model/View layer.

const (
	// DefaultLogsSubdir is appended under the app config directory.
	DefaultLogsSubdir = "logs"

	// DefaultLogExt is the extension used for daily logs.
	DefaultLogExt = ".log"

	// DefaultDayFormat controls the log filename date format.
	DefaultDayFormat = "2006-01-02"
)

// LogOptions controls how log file paths are computed and created.
type LogOptions struct {
	// BaseDir overrides the base logs directory. If empty, defaults to:
	//   $XDG_CONFIG_HOME/tmux-ssh-manager/logs
	// or:
	//   ~/.config/tmux-ssh-manager/logs
	BaseDir string

	// Timezone controls what "day" means for file rotation. If nil, local time is used.
	Timezone *time.Location

	// CreateParents, if true, ensures parent directories exist with restrictive perms.
	CreateParents bool

	// FilePerm is the permission mode used when creating a new file.
	// If 0, defaults to 0600.
	FilePerm os.FileMode

	// DirPerm is the permission mode used when creating parent directories.
	// If 0, defaults to 0700.
	DirPerm os.FileMode
}

// DefaultLogOptions returns conservative defaults.
func DefaultLogOptions() LogOptions {
	return LogOptions{
		BaseDir:        "",
		Timezone:       nil, // local
		CreateParents:  true,
		FilePerm:       0o600,
		DirPerm:        0o700,
	}
}

// HostLogInfo describes a per-host daily log location.
type HostLogInfo struct {
	HostKey     string
	Dir         string
	Date        string // YYYY-MM-DD
	Path        string // full path to YYYY-MM-DD.log
	Timezone    string
	Exists      bool
	SizeBytes   int64
	ModifiedUTC time.Time
}

// HostLogsBaseDir resolves the base logs directory according to opts and XDG rules.
func HostLogsBaseDir(opts LogOptions) (string, error) {
	if strings.TrimSpace(opts.BaseDir) != "" {
		return expandPath(strings.TrimSpace(opts.BaseDir)), nil
	}

	xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdg != "" {
		return filepath.Join(xdg, "tmux-ssh-manager", DefaultLogsSubdir), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "tmux-ssh-manager", DefaultLogsSubdir), nil
}

// HostLogDir returns the directory path for logs for a given host key.
func HostLogDir(hostKey string, opts LogOptions) (string, error) {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return "", errors.New("hostKey is required")
	}
	base, err := HostLogsBaseDir(opts)
	if err != nil {
		return "", err
	}
	safe := sanitizeHostKeyToFilename(hostKey)
	return filepath.Join(base, safe), nil
}

// DailyHostLogPath returns the log file path for the given host key and date.
// If t is zero, it uses time.Now().
func DailyHostLogPath(hostKey string, t time.Time, opts LogOptions) (string, error) {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return "", errors.New("hostKey is required")
	}
	if t.IsZero() {
		t = time.Now()
	}
	loc := opts.Timezone
	if loc == nil {
		loc = time.Local
	}
	day := t.In(loc).Format(DefaultDayFormat)

	dir, err := HostLogDir(hostKey, opts)
	if err != nil {
		return "", err
	}
	filename := day + DefaultLogExt
	return filepath.Join(dir, filename), nil
}

// EnsureDailyHostLog ensures the per-host daily log file exists and returns HostLogInfo.
// It creates parent directories and an empty file if needed (based on opts).
func EnsureDailyHostLog(hostKey string, t time.Time, opts LogOptions) (HostLogInfo, error) {
	if t.IsZero() {
		t = time.Now()
	}
	loc := opts.Timezone
	if loc == nil {
		loc = time.Local
	}
	opts = normalizeLogOptions(opts)

	p, err := DailyHostLogPath(hostKey, t, opts)
	if err != nil {
		return HostLogInfo{}, err
	}
	dir := filepath.Dir(p)

	if opts.CreateParents {
		if err := os.MkdirAll(dir, opts.DirPerm); err != nil {
			return HostLogInfo{}, fmt.Errorf("mkdir logs dir: %w", err)
		}
	}

	// Touch file
	f, err := os.OpenFile(p, os.O_CREATE, opts.FilePerm)
	if err != nil {
		return HostLogInfo{}, fmt.Errorf("create log file: %w", err)
	}
	_ = f.Close()

	info, _ := StatHostLogFile(p)
	info.HostKey = strings.TrimSpace(hostKey)
	info.Dir = dir
	info.Date = t.In(loc).Format(DefaultDayFormat)
	info.Path = p
	if loc != nil {
		info.Timezone = loc.String()
	}
	return info, nil
}

func normalizeLogOptions(opts LogOptions) LogOptions {
	if opts.FilePerm == 0 {
		opts.FilePerm = 0o600
	}
	if opts.DirPerm == 0 {
		opts.DirPerm = 0o700
	}
	return opts
}

// StatHostLogFile returns basic file info for a log file path.
// If the file does not exist, Exists will be false and error is nil.
func StatHostLogFile(path string) (HostLogInfo, error) {
	path = expandPath(strings.TrimSpace(path))
	if path == "" {
		return HostLogInfo{}, errors.New("path is empty")
	}
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return HostLogInfo{Path: path, Exists: false}, nil
		}
		return HostLogInfo{}, err
	}
	return HostLogInfo{
		Path:        path,
		Exists:      true,
		SizeBytes:   st.Size(),
		ModifiedUTC: st.ModTime().UTC(),
	}, nil
}

// AppendHostLogLine appends a single line to the host's daily log file.
// It adds a timestamp prefix in RFC3339 (local time) and ensures a trailing newline.
func AppendHostLogLine(hostKey string, t time.Time, opts LogOptions, line string) (HostLogInfo, error) {
	opts = normalizeLogOptions(opts)
	info, err := EnsureDailyHostLog(hostKey, t, opts)
	if err != nil {
		return HostLogInfo{}, err
	}

	loc := opts.Timezone
	if loc == nil {
		loc = time.Local
	}
	ts := t.In(loc).Format(time.RFC3339)

	// Keep one-line log records; callers can include structured content after this prefix.
	msg := strings.TrimRight(line, "\r\n")
	record := fmt.Sprintf("%s %s\n", ts, msg)

	f, err := os.OpenFile(info.Path, os.O_APPEND|os.O_WRONLY, opts.FilePerm)
	if err != nil {
		return HostLogInfo{}, fmt.Errorf("open log for append: %w", err)
	}
	defer f.Close()

	if _, err := io.WriteString(f, record); err != nil {
		return HostLogInfo{}, fmt.Errorf("write log: %w", err)
	}

	// Refresh file info best-effort
	if st, err := os.Stat(info.Path); err == nil {
		info.Exists = true
		info.SizeBytes = st.Size()
		info.ModifiedUTC = st.ModTime().UTC()
	}
	return info, nil
}

// ListHostLogFiles lists log files for the given host key, newest-first.
// It returns absolute paths.
func ListHostLogFiles(hostKey string, opts LogOptions) ([]string, error) {
	dir, err := HostLogDir(hostKey, opts)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, DefaultLogExt) {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	sortByDateFilenameDesc(paths)
	return paths, nil
}

// ReadLastNLines reads the last N lines of a file efficiently enough for typical log sizes.
// For very large logs, this does a bounded backward scan by blocks.
//
// Returns lines in normal order (oldest->newest within the returned window).
func ReadLastNLines(path string, n int) ([]string, error) {
	path = expandPath(strings.TrimSpace(path))
	if n <= 0 {
		return []string{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Try a backward scan. We'll read chunks from the end until we have enough newlines.
	const blockSize = 32 * 1024
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size == 0 {
		return []string{}, nil
	}

	var (
		buf      []byte
		offset   int64 = size
		newlines       = 0
	)
	for offset > 0 && newlines <= n {
		readSize := int64(blockSize)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		block := make([]byte, readSize)
		if _, err := io.ReadFull(f, block); err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		// Prepend to existing buffer
		buf = append(block, buf...)
		newlines = bytesCount(buf, '\n')
		// Bound memory a bit: if it gets too big, stop once we have enough lines.
		if int64(len(buf)) > 8*1024*1024 && newlines > n {
			break
		}
	}

	lines := splitLinesTrimRight(buf)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// ReadLogWindow reads a "window" of lines for UI paging.
// It reads up to limit lines starting from startLine (0-based) and returns the slice.
// This is a simple forward reader; for huge files, consider caching.
func ReadLogWindow(path string, startLine, limit int) ([]string, int, error) {
	path = expandPath(strings.TrimSpace(path))
	if limit <= 0 {
		return []string{}, 0, nil
	}
	if startLine < 0 {
		startLine = 0
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// allow long lines (device logs can be long)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	cur := 0
	out := make([]string, 0, limit)
	for sc.Scan() {
		if cur >= startLine && len(out) < limit {
			out = append(out, sc.Text())
		}
		cur++
		if len(out) >= limit && cur > startLine {
			// we can stop early
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	return out, cur, nil
}

// ---- internal helpers ----

func sortByDateFilenameDesc(paths []string) {
	// Sort by basename descending (YYYY-MM-DD.log lexicographically sorts correctly).
	// If different directories are present, include full path as tie-breaker.
	sort.Slice(paths, func(i, j int) bool {
		ai := filepath.Base(paths[i])
		aj := filepath.Base(paths[j])
		if ai == aj {
			return paths[i] > paths[j]
		}
		return ai > aj
	})
}

func bytesCount(b []byte, sep byte) int {
	c := 0
	for _, x := range b {
		if x == sep {
			c++
		}
	}
	return c
}

func splitLinesTrimRight(b []byte) []string {
	// Convert to string and split, trimming a single trailing newline.
	s := string(b)
	s = strings.TrimRight(s, "\r\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}
