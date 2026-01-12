package manager

import (
	"net"
	"strings"
)

// NormalizeHostFull returns a normalized representation of a hostname/system-name suitable
// for comparison. It is intentionally conservative and is meant for topology matching,
// not for DNS or SSH connection behavior.
//
// Normalization rules:
// - trim spaces
// - lower-case
// - remove a trailing dot (FQDN form)
// - strip surrounding brackets for IPv6 literals like "[2001:db8::1]"
func NormalizeHostFull(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Common: remote systems may be reported with a trailing dot.
	s = strings.TrimSuffix(s, ".")
	// IPv6 literals sometimes appear bracketed.
	if len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']' {
		s = s[1 : len(s)-1]
	}
	s = strings.TrimSpace(s)
	return strings.ToLower(s)
}

// NormalizeHostShort returns a "shortname" used for loose matching between discovered
// neighbor names and configured hosts.
//
// Rules (after NormalizeHostFull):
// - if it's an IP literal (v4 or v6), return the normalized IP string
// - otherwise, return the first DNS label (before the first '.')
func NormalizeHostShort(s string) string {
	full := NormalizeHostFull(s)
	if full == "" {
		return ""
	}

	// If it's an IP, normalize via net.ParseIP to avoid string-format mismatches.
	if ip := net.ParseIP(full); ip != nil {
		// Prefer canonical string form. (IPv6 will be compressed.)
		return ip.String()
	}

	// Hostname: take first label.
	if i := strings.IndexByte(full, '.'); i >= 0 {
		return full[:i]
	}
	return full
}

// HostNameMatches performs the requested matching strategy:
//
// - exact full-name match first (normalized)
// - else normalized shortname match
//
// This is intended for matching discovered remote nodes to configured hosts.
// It does not attempt regex, glob, or substring matching.
func HostNameMatches(configuredHostName, discoveredName string) bool {
	aFull := NormalizeHostFull(configuredHostName)
	bFull := NormalizeHostFull(discoveredName)
	if aFull == "" || bFull == "" {
		return false
	}
	if aFull == bFull {
		return true
	}
	return NormalizeHostShort(aFull) == NormalizeHostShort(bFull)
}

// ExtractIPsFromText returns a de-duplicated list of IP strings found in the text.
// This is a best-effort helper to support router-id based matching even before
// per-platform structured parsers exist.
//
// Notes:
// - This intentionally does not guarantee perfect IPv6 detection (textual ambiguity).
// - It should be used as an "identity hint" generator, not as a strict parser.
func ExtractIPsFromText(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Tokenize on common delimiters. Keep this cheap and dependency-free.
	splitter := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r',
			',', ';', '|',
			'(', ')', '[', ']', '{', '}',
			'<', '>', '"', '\'',
			'=':
			return true
		default:
			return false
		}
	}

	seen := make(map[string]struct{})
	var out []string
	for _, tok := range strings.FieldsFunc(text, splitter) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		// Trim trailing punctuation that often sticks to tokens.
		tok = strings.Trim(tok, ".,;")
		if tok == "" {
			continue
		}
		// Strip brackets for IPv6 forms like "[...]" or "(...)" already handled by splitter,
		// but keep this defensive.
		tok = strings.TrimPrefix(tok, "[")
		tok = strings.TrimSuffix(tok, "]")

		if ip := net.ParseIP(tok); ip != nil {
			n := ip.String()
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	return out
}

// ChooseIdentityIPs returns a de-duplicated set of IPs that should be used as identity hints
// when matching LLDP-discovered nodes to configured hosts.
//
// This intentionally supports BOTH:
// - mgmt IP (as discovered in LLDP output, e.g. MgmtIP fields)
// - router-id (a loopback identity address stored in host extras)
//
// Either may be present in device output depending on platform/VRF design. The topology
// discovery layer should treat a match on ANY returned IP as a positive identity match.
//
// Notes:
// - All inputs are validated via net.ParseIP and normalized to canonical string form.
// - Empty/invalid tokens are ignored.
// - The order is stable: routerID first (if valid), then mgmtIPs in their original order.
func ChooseIdentityIPs(routerID string, mgmtIPs []string) []string {
	seen := make(map[string]struct{})
	var out []string

	// Router-ID first (preferred stable identity).
	routerID = strings.TrimSpace(routerID)
	if routerID != "" {
		if ip := net.ParseIP(routerID); ip != nil {
			n := ip.String()
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}

	for _, tok := range mgmtIPs {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if ip := net.ParseIP(tok); ip != nil {
			n := ip.String()
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}

	return out
}
