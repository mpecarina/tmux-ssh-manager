package manager

import (
	"bufio"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file defines:
// - Network OS identifiers (for Host.network_os style selection)
// - LLDP command specs per OS
// - Parsers for LLDP neighbor outputs (Cisco IOS/XE and Dell SONiC)
// - Common data models used later by the network view graph builder
//
// Design goals:
// - Keep parsers tolerant of slightly messy CLI output
// - Preserve enough details for a useful diagram without overwhelming the user
// - Provide "identity hints" (IPs in text) so router-id / mgmt-ip can be used to match nodes
//
// NOTE: This file intentionally does NOT run SSH or tmux. It only provides command
// selection and parsing helpers.
//
// NOTE (Cisco IOS/XE capability handling):
// - For IOS/XE LLDP *detail* parsing we currently use "System Capabilities" (e.g. B,W,R,S)
//   and normalize single-letter tokens to stable labels (ROUTER/BRIDGE/etc).
// - This is an approximation-first implementation; if you later want "Enabled Capabilities"
//   to take precedence, we can adjust when/if a multi-neighbor parsing edge case appears.

// NetworkOS values. These should align with your YAML `network_os` values.
const (
	NetworkOSCiscoIOSXE = "cisco_iosxe"
	NetworkOSSonicDell  = "sonic_dell"
)

// LLDPCommandSpec defines how to retrieve neighbors on a device.
//
// Command should be a single remote command line suitable for `ssh host -- <command>`
// or a shell wrapper (as long as it's valid for the remote OS).
type LLDPCommandSpec struct {
	// OS identifies which device OS this spec applies to.
	OS string

	// Name is a human label for debugging and future UI.
	Name string

	// Command is the remote command to execute.
	Command string

	// Timeout is a best-effort suggested timeout (collection layer may override).
	Timeout time.Duration

	// Parser identifies which parser should be used on the output.
	ParserID string
}

// Parser IDs (to decouple command selection from parsing function names).
const (
	LLDPParserCiscoIOSXEShowNeighbors       = "cisco_iosxe_show_lldp_neighbors"
	LLDPParserCiscoIOSXEShowNeighborsDetail = "cisco_iosxe_show_lldp_neighbors_detail"
	LLDPParserCiscoIOSXEShowCDPNeighbors    = "cisco_iosxe_show_cdp_neighbors"

	LLDPParserSonicCLIShowNeighbors = "sonic_cli_show_lldp_neighbor"
)

// NeighborDiscoveryPreference allows per-host control over command ordering/fallback.
// Intended values are stable strings so they can be stored in per-host extras / settings.
//
// For example, Cisco IOS/XE can prefer CDP in environments where LLDP is disabled.
type NeighborDiscoveryPreference string

const (
	NeighborPrefAuto NeighborDiscoveryPreference = "auto" // default order per OS (e.g. LLDP detail -> LLDP -> CDP for IOS/XE)
	NeighborPrefLLDP NeighborDiscoveryPreference = "lldp" // prefer LLDP first (detail/summary), then CDP fallback
	NeighborPrefCDP  NeighborDiscoveryPreference = "cdp"  // prefer CDP first, then LLDP fallback
)

// DefaultLLDPCommands returns a prioritized list of neighbor-discovery command specs for the given OS.
// The caller should try specs in order until one succeeds (exit code 0 AND output parses).
//
// Cisco IOS/XE behavior (auto):
// - Prefer LLDP detail first (includes Mgmt IP / Management Addresses; best for identity matching).
// - Fall back to LLDP summary (table).
// - Finally, attempt CDP neighbors (useful in Cisco-heavy environments or where LLDP is disabled).
//
// Note: per-host preference (auto/lldp/cdp) is handled by DefaultNeighborCommandsForHost().
func DefaultLLDPCommands(os string) []LLDPCommandSpec {
	os = strings.ToLower(strings.TrimSpace(os))
	switch os {
	case NetworkOSCiscoIOSXE:
		return []LLDPCommandSpec{
			{
				OS:       NetworkOSCiscoIOSXE,
				Name:     "show lldp neighbors detail",
				Command:  "show lldp neighbors detail",
				Timeout:  12 * time.Second,
				ParserID: LLDPParserCiscoIOSXEShowNeighborsDetail,
			},
			{
				OS:       NetworkOSCiscoIOSXE,
				Name:     "show lldp neighbors",
				Command:  "show lldp neighbors",
				Timeout:  8 * time.Second,
				ParserID: LLDPParserCiscoIOSXEShowNeighbors,
			},
			{
				OS:       NetworkOSCiscoIOSXE,
				Name:     "show cdp neighbors",
				Command:  "show cdp neighbors",
				Timeout:  8 * time.Second,
				ParserID: LLDPParserCiscoIOSXEShowCDPNeighbors,
			},
		}
	case NetworkOSSonicDell:
		// Prefer plain `show lldp neighbors` which is generally available on SONiC and matches the parser expectations.
		// (sonic-cli output formats vary across builds; plain show output tends to be more deterministic.)
		return []LLDPCommandSpec{
			{
				OS:       NetworkOSSonicDell,
				Name:     "show lldp neighbors",
				Command:  "show lldp neighbors",
				Timeout:  10 * time.Second,
				ParserID: LLDPParserSonicCLIShowNeighbors,
			},
		}
	default:
		return nil
	}
}

// DefaultNeighborCommandsForHost returns the command specs to try for a given host,
// respecting an optional per-host preference string.
//
// pref values:
// - "auto" (or empty): use DefaultLLDPCommands(network_os)
// - "lldp": LLDP-only (skip CDP entirely)
// - "cdp" : CDP-only  (skip LLDP entirely)
//
// For non-IOS/XE OS families, pref currently behaves like "auto" unless extended later.
func DefaultNeighborCommandsForHost(networkOS string, pref string) []LLDPCommandSpec {
	networkOS = strings.ToLower(strings.TrimSpace(networkOS))
	p := NeighborDiscoveryPreference(strings.ToLower(strings.TrimSpace(pref)))
	if p == "" {
		p = NeighborPrefAuto
	}

	specs := DefaultLLDPCommands(networkOS)
	if networkOS != NetworkOSCiscoIOSXE {
		// For now, only IOS/XE supports an explicit LLDP/CDP filtering override.
		return specs
	}

	// Partition IOS/XE specs into LLDP vs CDP buckets, preserving relative order.
	var lldpSpecs []LLDPCommandSpec
	var cdpSpecs []LLDPCommandSpec
	for _, s := range specs {
		switch s.ParserID {
		case LLDPParserCiscoIOSXEShowCDPNeighbors:
			cdpSpecs = append(cdpSpecs, s)
		case LLDPParserCiscoIOSXEShowNeighbors, LLDPParserCiscoIOSXEShowNeighborsDetail:
			lldpSpecs = append(lldpSpecs, s)
		default:
			// Unknown parser id (future extension): treat as LLDP-family by default.
			lldpSpecs = append(lldpSpecs, s)
		}
	}

	switch p {
	case NeighborPrefLLDP:
		// Explicit LLDP-only: skip CDP entirely.
		return append([]LLDPCommandSpec(nil), lldpSpecs...)
	case NeighborPrefCDP:
		// Explicit CDP-only: skip LLDP entirely.
		return append([]LLDPCommandSpec(nil), cdpSpecs...)
	case NeighborPrefAuto:
		fallthrough
	default:
		// Existing default order from DefaultLLDPCommands (LLDP detail -> LLDP summary -> CDP for IOS/XE).
		return specs
	}
}

// LLDPNeighborEntry is a single neighbor relationship learned via LLDP.
// It is directional: "LocalDevice sees RemoteDevice via LocalPort -> RemotePort".
type LLDPNeighborEntry struct {
	// Local identifies the device we collected from (SSH target / configured host).
	LocalDevice string

	// LocalPort is the interface name on the local device (e.g. Gi2, Ethernet1).
	LocalPort string

	// Remote identifies the neighbor system name as reported by LLDP.
	RemoteDevice string

	// RemotePort is the neighbor port ID as reported by LLDP.
	RemotePort string

	// Capabilities is a normalized list of capabilities (e.g. ROUTER, BRIDGE).
	Capabilities []string

	// MgmtIPs is a list of management IPs discovered in the neighbor record (if present).
	// These are identity hints for router-id based matching.
	MgmtIPs []string

	// Raw is the raw record text (optional; useful for debugging / detail pane).
	Raw string
}

// LLDPParseResult is the output of parsing one LLDP command on one device.
type LLDPParseResult struct {
	// LocalDevice is the configured host name we collected from.
	LocalDevice string

	// Entries is the set of parsed LLDP edges/records.
	Entries []LLDPNeighborEntry

	// IdentityHintsIPs are IP literals found anywhere in the output (best-effort),
	// useful for matching router-id even if a platform doesn't present "MgmtIP" cleanly.
	IdentityHintsIPs []string

	// ParseWarnings are non-fatal issues that might help debug mismatches.
	ParseWarnings []string
}

// ParseLLDPOutput routes parsing based on parser ID.
func ParseLLDPOutput(parserID string, localDevice string, output string) (LLDPParseResult, error) {
	parserID = strings.ToLower(strings.TrimSpace(parserID))
	switch parserID {
	case LLDPParserCiscoIOSXEShowNeighbors:
		return ParseCiscoIOSXEShowLLDPNeighbors(localDevice, output)
	case LLDPParserCiscoIOSXEShowNeighborsDetail:
		return ParseCiscoIOSXEShowLLDPNeighborsDetail(localDevice, output)
	case LLDPParserCiscoIOSXEShowCDPNeighbors:
		return ParseCiscoIOSXEShowCDPNeighbors(localDevice, output)
	case LLDPParserSonicCLIShowNeighbors:
		return ParseSonicDellSonicCLIShowLLDPNeighbor(localDevice, output)
	default:
		return LLDPParseResult{}, fmt.Errorf("unknown lldp parser id: %q", parserID)
	}
}

func uniqStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func normalizeCapabilityToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToUpper(s)
	// Common synonyms/formatting.
	switch s {
	case "R":
		return "ROUTER"
	case "B":
		return "BRIDGE"
	case "T":
		return "TELEPHONE"
	case "W":
		return "WLAN_AP"
	case "P":
		return "REPEATER"
	case "S":
		return "STATION"
	case "O":
		return "OTHER"
	default:
		return s
	}
}

var (
	// Cisco "show lldp neighbors" is a table. We'll parse "Device ID ... Local Intf ... Port ID"
	// Example line:
	// sonic               Gi2            120        R               Ethernet1
	//
	// We'll be tolerant of multiple spaces and optional capability column formats.
	reCiscoLLDPRow = regexp.MustCompile(`^\s*(?P<device>\S+)\s+(?P<local>\S+)\s+(?P<hold>\d+)\s+(?P<caps>[A-Za-z,]+)\s+(?P<port>\S+)\s*$`)

	// Cisco "show lldp neighbors detail" block parsing.
	reCiscoDetailLocalIntf = regexp.MustCompile(`^\s*Local\s+Intf:\s*(?P<intf>\S+)\s*$`)
	reCiscoDetailPortID    = regexp.MustCompile(`^\s*Port\s+id:\s*(?P<port>\S+)\s*$`)
	reCiscoDetailSysName   = regexp.MustCompile(`^\s*System\s+Name:\s*(?P<name>.+?)\s*$`)
	reCiscoDetailSysCaps   = regexp.MustCompile(`^\s*System\s+Capabilities:\s*(?P<caps>.+?)\s*$`)
	reCiscoDetailMgmtIP    = regexp.MustCompile(`^\s*IP:\s*(?P<ip>\S+)\s*$`)

	// Cisco "show cdp neighbors" is also a table. We parse the common IOS-XE format:
	//
	// Device ID        Local Intrfce     Holdtme    Capability  Platform  Port ID
	// sonic            Gig 2             153        R S I       ...       Eth 1/1
	//
	// This parser is intentionally conservative: we primarily extract
	// - Device ID (remote system name)
	// - Local Interface (local port)
	// - Port ID (remote port)
	//
	// We do NOT depend on Platform/Capability columns being present/consistent.
	reCiscoCDPHeader = regexp.MustCompile(`^\s*Device\s+ID\s+Local\s+Intrfce.*Port\s+ID\s*$`)

	// A tolerant CDP row parser that works for common cases where:
	// - Device ID is a single token
	// - Local Intrfce is 1-2 tokens (e.g. "Gig 2" or "Gi2")
	// - Port ID is 1-3 tokens at end (e.g. "Eth 1/1" or "Ethernet1")
	//
	// We'll parse the row by tokenization rather than strict columns.
	// See ParseCiscoIOSXEShowCDPNeighbors for details.
	//
	// SONiC variants observed:
	// - "Interface: Ethernet0, via: LLDP, RID: 2, Time: ... "
	// - Optional extra fields after "via: LLDP" (RID/Time/etc)
	reSonicInterface = regexp.MustCompile(`^\s*Interface:\s*(?P<intf>[^,]+),\s*via:\s*LLDP(?:,.*)?\s*$`)
	reSonicSysName   = regexp.MustCompile(`^\s*SysName:\s*(?P<name>.+?)\s*$`)
	reSonicMgmtIP    = regexp.MustCompile(`^\s*MgmtIP:\s*(?P<ip>\S+)\s*$`)

	// PortID often contains multiple tokens (e.g. "local Ethernet0").
	// Capture the entire remainder and let the parser trim it.
	reSonicPortID = regexp.MustCompile(`^\s*PortID:\s*(?P<port>.+?)\s*$`)

	// Capability state may be "on/off" in addition to "ON/OFF".
	reSonicCap = regexp.MustCompile(`^\s*Capability:\s*(?P<cap>[^,]+),\s*(?P<state>ON|OFF|on|off)\s*$`)
)

// ParseCiscoIOSXEShowLLDPNeighbors parses Cisco IOS/XE:
//
// Router#show lldp neighbors
// ...
// Device ID           Local Intf     Hold-time  Capability      Port ID
// sonic               Gi2            120        R               Ethernet1
// ...
func ParseCiscoIOSXEShowLLDPNeighbors(localDevice string, output string) (LLDPParseResult, error) {
	localDevice = strings.TrimSpace(localDevice)
	out := LLDPParseResult{
		LocalDevice:      localDevice,
		Entries:          nil,
		IdentityHintsIPs: ExtractIPsFromText(output),
		ParseWarnings:    nil,
	}

	// Find the header line, then parse until blank line or "Total entries".
	sc := bufio.NewScanner(strings.NewReader(output))
	inTable := false
	var warnings []string
	var entries []LLDPNeighborEntry

	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)

		// Start: detect header
		if !inTable {
			if strings.HasPrefix(trim, "Device ID") && strings.Contains(trim, "Local Intf") && strings.Contains(trim, "Port ID") {
				inTable = true
			}
			continue
		}

		// Stop conditions
		if trim == "" {
			// table often ends with a blank line
			break
		}
		if strings.HasPrefix(trim, "Total entries") {
			break
		}
		// Skip capability codes preface if it appears after we toggled inTable incorrectly.
		if strings.HasPrefix(trim, "Capability codes") {
			continue
		}

		m := reCiscoLLDPRow.FindStringSubmatch(line)
		if m == nil {
			// tolerate odd lines; track warning but keep going
			// (e.g. wrapped output in narrow terminals)
			warnings = append(warnings, "unparsed row: "+trim)
			continue
		}

		device := strings.TrimSpace(m[reCiscoLLDPRow.SubexpIndex("device")])
		localIntf := strings.TrimSpace(m[reCiscoLLDPRow.SubexpIndex("local")])
		capsRaw := strings.TrimSpace(m[reCiscoLLDPRow.SubexpIndex("caps")])
		remotePort := strings.TrimSpace(m[reCiscoLLDPRow.SubexpIndex("port")])

		caps := []string{}
		for _, tok := range strings.Split(capsRaw, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			// Cisco outputs single letters (R/B/...) in this table.
			caps = append(caps, normalizeCapabilityToken(tok))
		}
		caps = uniqStrings(caps)

		entries = append(entries, LLDPNeighborEntry{
			LocalDevice:  localDevice,
			LocalPort:    localIntf,
			RemoteDevice: device,
			RemotePort:   remotePort,
			Capabilities: caps,
			MgmtIPs:      nil, // Cisco table output here doesn't include mgmt IP.
			Raw:          trim,
		})
	}

	if err := sc.Err(); err != nil {
		return LLDPParseResult{}, fmt.Errorf("scan output: %w", err)
	}

	out.Entries = entries
	out.ParseWarnings = uniqStrings(append(out.ParseWarnings, warnings...))

	// If we never found the table header and there are no entries, treat as parse error.
	if len(out.Entries) == 0 && !strings.Contains(output, "Device ID") {
		return LLDPParseResult{}, fmt.Errorf("cisco iosxe lldp parse: missing expected header")
	}

	return out, nil
}

// ParseCiscoIOSXEShowLLDPNeighborsDetail parses Cisco IOS/XE:
//
// Router#show lldp neighbors detail
// ------------------------------------------------
// Local Intf: Gi2
// Chassis id: 5254.003f.e750
// Port id: Ethernet1
// Port Description: Eth1/2
// System Name: sonic
// ...
// System Capabilities: B,W,R,S
// Enabled Capabilities: R
// Management Addresses:
//
//	IP: 192.168.129.174
//
// ...
//
// Total entries displayed: 1
func ParseCiscoIOSXEShowLLDPNeighborsDetail(localDevice string, output string) (LLDPParseResult, error) {
	localDevice = strings.TrimSpace(localDevice)
	res := LLDPParseResult{
		LocalDevice:      localDevice,
		Entries:          nil,
		IdentityHintsIPs: ExtractIPsFromText(output),
		ParseWarnings:    nil,
	}

	sc := bufio.NewScanner(strings.NewReader(output))

	type blk struct {
		localIntf   string
		remotePort  string
		remoteName  string
		caps        []string
		mgmtIPs     []string
		rawLines    []string
		inMgmtAddrs bool
	}

	flush := func(b *blk) {
		if b == nil {
			return
		}
		if strings.TrimSpace(b.localIntf) == "" || strings.TrimSpace(b.remoteName) == "" {
			return
		}
		entry := LLDPNeighborEntry{
			LocalDevice:  localDevice,
			LocalPort:    strings.TrimSpace(b.localIntf),
			RemoteDevice: strings.TrimSpace(b.remoteName),
			RemotePort:   strings.TrimSpace(b.remotePort),
			Capabilities: uniqStrings(b.caps),
			MgmtIPs:      uniqStrings(b.mgmtIPs),
			Raw:          strings.TrimSpace(strings.Join(b.rawLines, "\n")),
		}
		res.Entries = append(res.Entries, entry)
	}

	var b *blk
	var warnings []string

	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)

		// End-of-output footer.
		if strings.HasPrefix(trim, "Total entries") {
			break
		}

		// Block separator line indicates "new block" in many outputs. We'll use Local Intf as the real delimiter.
		if b != nil {
			b.rawLines = append(b.rawLines, line)
		}

		if m := reCiscoDetailLocalIntf.FindStringSubmatch(line); m != nil {
			// New block start: flush previous
			flush(b)
			b = &blk{
				localIntf: strings.TrimSpace(m[reCiscoDetailLocalIntf.SubexpIndex("intf")]),
				rawLines:  []string{line},
			}
			continue
		}

		if b == nil {
			// Ignore until first Local Intf.
			continue
		}

		if m := reCiscoDetailPortID.FindStringSubmatch(line); m != nil {
			b.remotePort = strings.TrimSpace(m[reCiscoDetailPortID.SubexpIndex("port")])
			continue
		}
		if m := reCiscoDetailSysName.FindStringSubmatch(line); m != nil {
			b.remoteName = strings.TrimSpace(m[reCiscoDetailSysName.SubexpIndex("name")])
			continue
		}
		if m := reCiscoDetailSysCaps.FindStringSubmatch(line); m != nil {
			// NOTE: we intentionally use "System Capabilities" here (approximation-first).
			// If you later want "Enabled Capabilities" to take precedence, we can adjust.
			//
			// Example: B,W,R,S
			rawCaps := strings.TrimSpace(m[reCiscoDetailSysCaps.SubexpIndex("caps")])
			for _, tok := range strings.Split(rawCaps, ",") {
				tok = strings.TrimSpace(tok)
				if tok == "" {
					continue
				}
				// Cisco detail uses letters with commas; map to normalized capability names.
				b.caps = append(b.caps, normalizeCapabilityToken(tok))
			}
			continue
		}

		// Management addresses section begins with a header, followed by indented "IP: x"
		if strings.EqualFold(trim, "Management Addresses:") {
			b.inMgmtAddrs = true
			continue
		}
		if b.inMgmtAddrs {
			// Stop mgmt section when we hit a non-indented or a known next section header.
			// (Be conservative: if we see something like "Auto Negotiation" or "MED Information", exit mgmt mode.)
			if strings.HasPrefix(trim, "Auto Negotiation") || strings.HasPrefix(trim, "Physical media") || strings.HasPrefix(trim, "MED Information") || strings.HasPrefix(trim, "Time remaining") || strings.HasPrefix(trim, "Vlan ID") {
				b.inMgmtAddrs = false
				// fallthrough to allow other parsing on this line
			} else {
				if m := reCiscoDetailMgmtIP.FindStringSubmatch(line); m != nil {
					ipTok := strings.TrimSpace(m[reCiscoDetailMgmtIP.SubexpIndex("ip")])
					if ip := net.ParseIP(ipTok); ip != nil {
						b.mgmtIPs = append(b.mgmtIPs, ip.String())
					} else if ipTok != "" {
						warnings = append(warnings, "cisco detail: invalid mgmt ip token: "+ipTok)
					}
					continue
				}
				// If we see another indented key that isn't IP:, keep mgmt mode but ignore.
				if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
					continue
				}
				// Non-indented line ends mgmt section.
				b.inMgmtAddrs = false
			}
		}
	}

	if err := sc.Err(); err != nil {
		return LLDPParseResult{}, fmt.Errorf("scan output: %w", err)
	}

	flush(b)
	res.ParseWarnings = uniqStrings(append(res.ParseWarnings, warnings...))

	// If we expected detail output but found nothing that looks like it, return error so caller can fall back.
	if len(res.Entries) == 0 && !strings.Contains(output, "Local Intf:") {
		return LLDPParseResult{}, fmt.Errorf("cisco iosxe lldp detail parse: missing expected Local Intf blocks")
	}

	return res, nil
}

// ParseCiscoIOSXEShowCDPNeighbors parses Cisco IOS/XE:
//
// Router#show cdp neighbors
// Device ID        Local Intrfce     Holdtme    Capability  Platform  Port ID
// sonic            Gig 2             153        R S I       ...       Eth 1/1
//
// This is used as an optional fallback when LLDP is disabled or unavailable.
// Parsing is approximation-first:
// - RemoteDevice := Device ID (first token)
// - LocalPort    := Local Intrfce (best-effort)
// - RemotePort   := Port ID (best-effort; last tokens)
// - Capabilities := not reliably parsed (varies widely); left empty for now
//
// Mgmt IP is not present in this command output; identity matching can still work via:
// - router-id/mgmt-ip extras on configured hosts
// - IPs extracted from other command outputs (if any)
func ParseCiscoIOSXEShowCDPNeighbors(localDevice string, output string) (LLDPParseResult, error) {
	localDevice = strings.TrimSpace(localDevice)
	res := LLDPParseResult{
		LocalDevice:      localDevice,
		Entries:          nil,
		IdentityHintsIPs: ExtractIPsFromText(output),
		ParseWarnings:    nil,
	}

	sc := bufio.NewScanner(strings.NewReader(output))

	inTable := false
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}

		if !inTable {
			if reCiscoCDPHeader.MatchString(line) {
				inTable = true
			}
			continue
		}

		// Common footer variants; be tolerant.
		if strings.HasPrefix(trim, "Total") && strings.Contains(trim, "entries") {
			break
		}

		// Tokenize. CDP is column-ish but capabilities/platform fields can contain spaces.
		// We'll do a minimal strategy:
		// - first token is device id
		// - last 1-3 tokens are remote port id (joined)
		// - next 1-2 tokens after device id are local interface (joined)
		toks := strings.Fields(line)
		if len(toks) < 4 {
			// Too short to be a neighbor row.
			continue
		}

		remoteDev := toks[0]

		// Heuristic: remote port is usually last 2 tokens (e.g. "Eth 1/1") or last 1 token (e.g. "Ethernet1").
		remotePort := ""
		if len(toks) >= 2 {
			// Try last 2 tokens first.
			rp2 := toks[len(toks)-2] + " " + toks[len(toks)-1]
			// If last token looks like a pure number, keep 2 tokens; otherwise 1 may be enough.
			// This is approximate; we accept either.
			remotePort = strings.TrimSpace(rp2)
			if strings.Count(remotePort, " ") == 1 {
				// If the first part is something like "Eth" or "Gig", good; else we may still keep it.
			}
		}
		// If remotePort is still empty (shouldn't happen), set to last token.
		if strings.TrimSpace(remotePort) == "" {
			remotePort = toks[len(toks)-1]
		}

		// Local interface is typically tokens[1] or tokens[1]+" "+tokens[2] (e.g. "Gig 2").
		localPort := toks[1]
		if len(toks) >= 3 {
			// If token[2] looks like a number/subport, include it.
			if _, err := strconv.Atoi(toks[2]); err == nil {
				localPort = toks[1] + " " + toks[2]
			}
		}

		res.Entries = append(res.Entries, LLDPNeighborEntry{
			LocalDevice:  localDevice,
			LocalPort:    strings.TrimSpace(localPort),
			RemoteDevice: strings.TrimSpace(remoteDev),
			RemotePort:   strings.TrimSpace(remotePort),
			Capabilities: nil,
			MgmtIPs:      nil,
			Raw:          strings.TrimSpace(line),
		})
	}

	if err := sc.Err(); err != nil {
		return LLDPParseResult{}, fmt.Errorf("scan output: %w", err)
	}

	// If this doesn't even look like CDP output, return an error so other specs can be tried.
	if len(res.Entries) == 0 && !strings.Contains(output, "Device ID") {
		return LLDPParseResult{}, fmt.Errorf("cisco iosxe cdp parse: missing expected header")
	}

	return res, nil
}

// ParseSonicDellSonicCLIShowLLDPNeighbor parses Dell SONiC output from:
//
//	show lldp neighbors
//
// (Historically we targeted `sonic-cli -b -c "show lldp neighbor"` but that output can vary;
// the plain `show lldp neighbors` output tends to be more consistent across SONiC builds.)
//
// It typically includes one block per local interface. We parse minimal useful fields:
// - Interface (local port)
// - SysName (remote device)
// - MgmtIP (remote mgmt ip)
// - PortID (remote port id)
// - Capability lines (router/bridge, etc)
//
// Example block (abridged):
// Interface:    Ethernet0, via: LLDP, RID: 2, Time: 4 days, 16:32:36
// ...
// SysName:      sonic
// ...
// MgmtIP:       192.168.126.50
// Capability:   Router, on
// ...
// PortID:       local Ethernet0
func ParseSonicDellSonicCLIShowLLDPNeighbor(localDevice string, output string) (LLDPParseResult, error) {
	localDevice = strings.TrimSpace(localDevice)
	res := LLDPParseResult{
		LocalDevice:      localDevice,
		Entries:          nil,
		IdentityHintsIPs: ExtractIPsFromText(output),
		ParseWarnings:    nil,
	}

	sc := bufio.NewScanner(strings.NewReader(output))

	type curBlock struct {
		localIntf  string
		remoteName string
		remotePort string
		mgmtIPs    []string
		caps       []string
		rawLines   []string
	}
	flush := func(b *curBlock) {
		if b == nil {
			return
		}
		// Only emit if we have at least localIntf + remoteName.
		if strings.TrimSpace(b.localIntf) == "" || strings.TrimSpace(b.remoteName) == "" {
			return
		}
		entry := LLDPNeighborEntry{
			LocalDevice:  localDevice,
			LocalPort:    strings.TrimSpace(b.localIntf),
			RemoteDevice: strings.TrimSpace(b.remoteName),
			RemotePort:   strings.TrimSpace(b.remotePort),
			MgmtIPs:      uniqStrings(b.mgmtIPs),
			Capabilities: uniqStrings(b.caps),
			Raw:          strings.TrimSpace(strings.Join(b.rawLines, "\n")),
		}
		res.Entries = append(res.Entries, entry)
	}

	var b *curBlock
	var warnings []string

	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if trim == "" {
			// Keep blank lines in raw for readability; doesn't end a block necessarily.
			if b != nil {
				b.rawLines = append(b.rawLines, line)
			}
			continue
		}

		// Detect new interface block start.
		if m := reSonicInterface.FindStringSubmatch(line); m != nil {
			// flush previous
			flush(b)
			b = &curBlock{
				localIntf: strings.TrimSpace(m[reSonicInterface.SubexpIndex("intf")]),
				rawLines:  []string{line},
			}
			continue
		}

		// Ignore banner separators but keep them from breaking parsing.
		if strings.HasPrefix(trim, "-----") {
			if b != nil {
				b.rawLines = append(b.rawLines, line)
			}
			continue
		}

		// If we haven't started a block yet, ignore until an Interface line.
		if b == nil {
			continue
		}

		b.rawLines = append(b.rawLines, line)

		if m := reSonicSysName.FindStringSubmatch(line); m != nil {
			b.remoteName = strings.TrimSpace(m[reSonicSysName.SubexpIndex("name")])
			continue
		}
		if m := reSonicMgmtIP.FindStringSubmatch(line); m != nil {
			ipTok := strings.TrimSpace(m[reSonicMgmtIP.SubexpIndex("ip")])
			// Only accept if it parses.
			if ip := net.ParseIP(ipTok); ip != nil {
				b.mgmtIPs = append(b.mgmtIPs, ip.String())
			} else if ipTok != "" {
				warnings = append(warnings, "sonic: invalid MgmtIP token: "+ipTok)
			}
			continue
		}
		if m := reSonicPortID.FindStringSubmatch(line); m != nil {
			b.remotePort = strings.TrimSpace(m[reSonicPortID.SubexpIndex("port")])
			continue
		}
		if m := reSonicCap.FindStringSubmatch(line); m != nil {
			cap := strings.TrimSpace(m[reSonicCap.SubexpIndex("cap")])
			state := strings.TrimSpace(m[reSonicCap.SubexpIndex("state")])
			// Only include if ON.
			if strings.EqualFold(state, "ON") {
				b.caps = append(b.caps, normalizeCapabilityToken(cap))
			}
			continue
		}
	}

	if err := sc.Err(); err != nil {
		return LLDPParseResult{}, fmt.Errorf("scan output: %w", err)
	}

	// flush last
	flush(b)

	res.ParseWarnings = uniqStrings(append(res.ParseWarnings, warnings...))

	// If we couldn't find any interface blocks but output looks like SONiC LLDP, return a parse error.
	// Note: SONiC may print "LLDP neighbors" (lowercase n) depending on build.
	if len(res.Entries) == 0 &&
		(strings.Contains(output, "LLDP Neighbors") || strings.Contains(output, "LLDP neighbors")) &&
		strings.Contains(output, "Interface:") {
		return LLDPParseResult{}, fmt.Errorf("sonic lldp parse: no neighbor entries parsed")
	}
	return res, nil
}
