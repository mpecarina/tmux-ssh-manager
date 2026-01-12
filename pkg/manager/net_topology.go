package manager

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// This file builds a topology graph from per-device LLDP parse results.
//
// It focuses on:
// - Matching discovered neighbor nodes to configured hosts using:
//   1) exact full-name match (normalized)
//   2) normalized shortname match
//   3) identity IP match (MgmtIP and/or RouterID from HostExtras) against IPs found in LLDP outputs
// - Creating first-class nodes for configured hosts and downregulated nodes for unknown discovered systems
// - Marking configured devices that have no LLDP edges (due to command failure or isolation) as "islands"
//
// Collection (SSH execution) and rendering (TUI diagram) are intentionally NOT in this file.

type TopologyNodeKind int

const (
	NodeUnknown TopologyNodeKind = iota
	NodeConfigured
)

// TopologyNode represents a device/system in the topology.
type TopologyNode struct {
	ID string // stable internal ID (typically canonical name or "unknown:<name>")

	Kind TopologyNodeKind

	// Label is the preferred display label (may be abbreviated by renderer).
	Label string

	// KnownHost is set when Kind==NodeConfigured.
	KnownHost *Host

	// KnownResolved is set when Kind==NodeConfigured (resolved settings).
	KnownResolved *ResolvedHost

	// Extras are available when Kind==NodeConfigured (best-effort; may be zero value if load failed).
	Extras HostExtras

	// DiscoveredNames are the raw names seen in LLDP output for this node.
	DiscoveredNames []string

	// IdentityIPs are IPs associated with this node (from HostExtras for configured nodes,
	// and/or from LLDP mgmt IPs / output scans for discovered nodes).
	IdentityIPs []string

	// HasLLDPData means we successfully parsed LLDP output for this local device (when configured).
	// For unknown nodes, this indicates the node was created due to LLDP discovery.
	HasLLDPData bool

	// Island indicates a configured node exists in the selected set but has zero edges
	// (either no neighbors or collection/parsing failed).
	Island bool

	// Errors/warnings are intended for the detail pane/debug views.
	Errors   []string
	Warnings []string
}

// TopologyEdge represents a directional LLDP adjacency (local -> remote).
type TopologyEdge struct {
	FromNodeID string
	ToNodeID   string

	LocalPort  string
	RemotePort string

	Capabilities []string
	MgmtIPs      []string

	// Raw retains a compact raw record (optional).
	Raw string
}

// TopologyGraph is the computed topology for the selected devices.
type TopologyGraph struct {
	Nodes map[string]*TopologyNode
	Edges []TopologyEdge

	// Convenience indexes for UI.
	AdjOut map[string][]int // nodeID -> indices into Edges
	AdjIn  map[string][]int // nodeID -> indices into Edges
}

// BuildTopologyOptions controls matching behavior.
type BuildTopologyOptions struct {
	// If true, create unknown nodes for remote neighbors even if they match no configured host.
	IncludeUnknown bool

	// If true, configured nodes that have no edges are included and marked Island=true.
	IncludeIslands bool

	// If true, attempt router-id / mgmt-ip identity matching using IP hints.
	EnableIPIdentityMatching bool
}

func DefaultBuildTopologyOptions() BuildTopologyOptions {
	return BuildTopologyOptions{
		IncludeUnknown:           true,
		IncludeIslands:           true,
		EnableIPIdentityMatching: true,
	}
}

// BuildTopologyGraph builds a TopologyGraph from LLDP results.
//
// Inputs:
// - cfg: full config (used to resolve discovered nodes to configured hosts)
// - selected: the set of configured hosts the user selected as "roots" for discovery
// - results: LLDP parse results, one per selected host (successful or failed)
//
// Notes:
// - This does NOT require that results cover all cfg.Hosts; it is scoped to selected roots.
// - If a selected host has no entries due to failure/no neighbors, it becomes an Island (if enabled).
func BuildTopologyGraph(cfg *Config, selected []ResolvedHost, results []LLDPParseResult, opts BuildTopologyOptions) (*TopologyGraph, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	if opts == (BuildTopologyOptions{}) {
		opts = DefaultBuildTopologyOptions()
	}

	g := &TopologyGraph{
		Nodes:  make(map[string]*TopologyNode),
		Edges:  nil,
		AdjOut: make(map[string][]int),
		AdjIn:  make(map[string][]int),
	}

	// Index configured hosts for fast matching.
	idx := buildConfiguredIndex(cfg)

	// Ensure selected configured nodes exist even before edges (so they can become islands).
	selectedIDs := make(map[string]struct{}, len(selected))
	for i := range selected {
		r := selected[i]
		n := ensureConfiguredNode(g, cfg, idx, r.Host.Name)
		selectedIDs[n.ID] = struct{}{}
	}

	// Map LLDP results by local device name (normalized) for quick lookup.
	resByLocal := make(map[string]LLDPParseResult)
	for _, r := range results {
		k := NormalizeHostFull(r.LocalDevice)
		if k == "" {
			continue
		}
		// Last wins; collection layer should provide one result per device anyway.
		resByLocal[k] = r
	}

	// Build edges.
	for _, sel := range selected {
		localName := sel.Host.Name
		localNode := ensureConfiguredNode(g, cfg, idx, localName)

		r, ok := resByLocal[NormalizeHostFull(localName)]
		if !ok {
			// No result: treat as failed collection -> island candidate.
			localNode.Errors = append(localNode.Errors, "lldp: no result (collection not run or failed)")
			localNode.HasLLDPData = false
			continue
		}

		localNode.HasLLDPData = true
		if len(r.ParseWarnings) > 0 {
			localNode.Warnings = append(localNode.Warnings, r.ParseWarnings...)
		}

		// Build one directional edge per parsed entry.
		for _, e := range r.Entries {
			// Sanity: ensure local matches.
			if NormalizeHostFull(e.LocalDevice) != NormalizeHostFull(localName) && strings.TrimSpace(e.LocalDevice) != "" {
				// Keep going; use selected host as authoritative local.
			}

			remoteNode := resolveOrCreateRemoteNode(g, cfg, idx, e, r, opts)
			if remoteNode == nil {
				continue
			}

			edge := TopologyEdge{
				FromNodeID:   localNode.ID,
				ToNodeID:     remoteNode.ID,
				LocalPort:    strings.TrimSpace(e.LocalPort),
				RemotePort:   strings.TrimSpace(e.RemotePort),
				Capabilities: dedupNonEmpty(e.Capabilities),
				MgmtIPs:      dedupIPs(e.MgmtIPs),
				Raw:          strings.TrimSpace(e.Raw),
			}
			g.Edges = append(g.Edges, edge)
			idxEdge := len(g.Edges) - 1
			g.AdjOut[edge.FromNodeID] = append(g.AdjOut[edge.FromNodeID], idxEdge)
			g.AdjIn[edge.ToNodeID] = append(g.AdjIn[edge.ToNodeID], idxEdge)
		}
	}

	// Mark islands: selected configured nodes with no edges out (and optionally in) are islands.
	if opts.IncludeIslands {
		for nodeID := range selectedIDs {
			n := g.Nodes[nodeID]
			if n == nil || n.Kind != NodeConfigured {
				continue
			}
			outCount := len(g.AdjOut[nodeID])
			inCount := len(g.AdjIn[nodeID])
			if outCount == 0 && inCount == 0 {
				n.Island = true
				// Downregulate: renderer should treat Island similar to unknown nodes, but still configured.
			}
		}
	} else {
		// Optionally remove configured nodes that are edge-less.
		for nodeID := range selectedIDs {
			n := g.Nodes[nodeID]
			if n == nil || n.Kind != NodeConfigured {
				continue
			}
			if len(g.AdjOut[nodeID]) == 0 && len(g.AdjIn[nodeID]) == 0 {
				delete(g.Nodes, nodeID)
			}
		}
	}

	// If unknown nodes are disabled, strip them and any edges referencing them.
	if !opts.IncludeUnknown {
		filteredEdges := make([]TopologyEdge, 0, len(g.Edges))
		for _, e := range g.Edges {
			fn := g.Nodes[e.FromNodeID]
			tn := g.Nodes[e.ToNodeID]
			if fn == nil || tn == nil {
				continue
			}
			if fn.Kind == NodeUnknown || tn.Kind == NodeUnknown {
				continue
			}
			filteredEdges = append(filteredEdges, e)
		}
		g.Edges = filteredEdges
		// Rebuild adjacencies.
		g.AdjOut = make(map[string][]int)
		g.AdjIn = make(map[string][]int)
		for i := range g.Edges {
			e := g.Edges[i]
			g.AdjOut[e.FromNodeID] = append(g.AdjOut[e.FromNodeID], i)
			g.AdjIn[e.ToNodeID] = append(g.AdjIn[e.ToNodeID], i)
		}
		// Remove unknown nodes.
		for id, n := range g.Nodes {
			if n != nil && n.Kind == NodeUnknown {
				delete(g.Nodes, id)
			}
		}
	}

	return g, nil
}

// ---- Matching + node creation helpers ----

type configuredIndex struct {
	// Full normalized name -> host pointer
	byFull map[string]*Host
	// Short normalized name -> list (collisions possible)
	byShort map[string][]*Host
	// Identity IP (canonical) -> host pointer (if multiple, first wins; collisions are ambiguous)
	byIP map[string]*Host
}

func buildConfiguredIndex(cfg *Config) *configuredIndex {
	idx := &configuredIndex{
		byFull:  make(map[string]*Host),
		byShort: make(map[string][]*Host),
		byIP:    make(map[string]*Host),
	}
	for i := range cfg.Hosts {
		h := &cfg.Hosts[i]
		full := NormalizeHostFull(h.Name)
		if full == "" {
			continue
		}
		// First wins for byFull; config validation should ensure unique names if desired.
		if _, ok := idx.byFull[full]; !ok {
			idx.byFull[full] = h
		}

		short := NormalizeHostShort(h.Name)
		if short != "" {
			idx.byShort[short] = append(idx.byShort[short], h)
		}

		// Identity IPs from extras (best-effort).
		ex, err := LoadHostExtras(h.Name)
		if err == nil {
			ips := ChooseIdentityIPs(ex.RouterID, []string{ex.MgmtIP})
			for _, ipStr := range ips {
				if ip := net.ParseIP(ipStr); ip != nil {
					key := ip.String()
					if _, exists := idx.byIP[key]; !exists {
						idx.byIP[key] = h
					}
				}
			}
		}
	}
	return idx
}

func ensureConfiguredNode(g *TopologyGraph, cfg *Config, idx *configuredIndex, hostName string) *TopologyNode {
	// Resolve configured host by name (exact/short) in a conservative manner.
	h := resolveConfiguredHostByName(idx, hostName)
	if h == nil {
		// Fallback: create unknown node (but label as the provided name).
		return ensureUnknownNode(g, hostName)
	}

	id := "cfg:" + NormalizeHostFull(h.Name)
	if n := g.Nodes[id]; n != nil {
		return n
	}

	r := cfg.ResolveEffective(*h)
	ex, _ := LoadHostExtras(h.Name)

	n := &TopologyNode{
		ID:              id,
		Kind:            NodeConfigured,
		Label:           h.Name,
		KnownHost:       h,
		KnownResolved:   &r,
		Extras:          ex,
		DiscoveredNames: nil,
		IdentityIPs:     ChooseIdentityIPs(ex.RouterID, []string{ex.MgmtIP}),
		HasLLDPData:     false,
		Island:          false,
		Errors:          nil,
		Warnings:        nil,
	}
	g.Nodes[id] = n
	return n
}

func ensureUnknownNode(g *TopologyGraph, discoveredName string) *TopologyNode {
	full := NormalizeHostFull(discoveredName)
	if full == "" {
		full = strings.TrimSpace(discoveredName)
	}
	id := "unk:" + full
	if n := g.Nodes[id]; n != nil {
		// Add to discovered names set if needed.
		if discoveredName != "" {
			n.DiscoveredNames = appendUnique(n.DiscoveredNames, discoveredName)
		}
		return n
	}

	n := &TopologyNode{
		ID:              id,
		Kind:            NodeUnknown,
		Label:           strings.TrimSpace(discoveredName),
		KnownHost:       nil,
		KnownResolved:   nil,
		IdentityIPs:     nil,
		DiscoveredNames: dedupNonEmpty([]string{discoveredName}),
		HasLLDPData:     true,  // it exists because it was discovered
		Island:          false, // unknown nodes are never "islands" in the configured sense
		Errors:          nil,
		Warnings:        nil,
	}
	g.Nodes[id] = n
	return n
}

func resolveOrCreateRemoteNode(g *TopologyGraph, cfg *Config, idx *configuredIndex, entry LLDPNeighborEntry, parseRes LLDPParseResult, opts BuildTopologyOptions) *TopologyNode {
	remoteName := strings.TrimSpace(entry.RemoteDevice)

	// Attempt name-based match first.
	if h := resolveConfiguredHostByName(idx, remoteName); h != nil {
		return ensureConfiguredNode(g, cfg, idx, h.Name)
	}

	// If enabled, attempt identity IP matching using:
	// - MgmtIPs on this entry
	// - Any IPs in the entire output (IdentityHintsIPs)
	if opts.EnableIPIdentityMatching {
		candidates := make([]string, 0, len(entry.MgmtIPs)+len(parseRes.IdentityHintsIPs))
		candidates = append(candidates, entry.MgmtIPs...)
		candidates = append(candidates, parseRes.IdentityHintsIPs...)
		for _, ipStr := range dedupIPs(candidates) {
			if h := resolveConfiguredHostByIP(idx, ipStr); h != nil {
				return ensureConfiguredNode(g, cfg, idx, h.Name)
			}
		}
	}

	// Unknown neighbor.
	if !opts.IncludeUnknown {
		return nil
	}
	n := ensureUnknownNode(g, remoteName)
	// Track identity IPs discovered for unknown nodes (for potential later re-matching).
	n.IdentityIPs = dedupIPs(append(n.IdentityIPs, entry.MgmtIPs...))
	return n
}

func resolveConfiguredHostByName(idx *configuredIndex, discoveredName string) *Host {
	if idx == nil {
		return nil
	}
	discoveredName = strings.TrimSpace(discoveredName)
	if discoveredName == "" {
		return nil
	}

	// Exact full-name match (normalized) first.
	full := NormalizeHostFull(discoveredName)
	if full != "" {
		if h := idx.byFull[full]; h != nil {
			return h
		}
	}

	// Shortname match second.
	short := NormalizeHostShort(discoveredName)
	if short != "" {
		cands := idx.byShort[short]
		if len(cands) == 1 {
			return cands[0]
		}
		// If multiple, try to disambiguate by full normalization if possible.
		if len(cands) > 1 && full != "" {
			for _, h := range cands {
				if NormalizeHostFull(h.Name) == full {
					return h
				}
			}
			// Else ambiguous: return nil to avoid wrong matches.
			return nil
		}
	}
	return nil
}

func resolveConfiguredHostByIP(idx *configuredIndex, ipStr string) *Host {
	if idx == nil {
		return nil
	}
	ipStr = strings.TrimSpace(ipStr)
	if ipStr == "" {
		return nil
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil
	}
	return idx.byIP[ip.String()]
}

// ---- Utility helpers ----

func appendUnique(in []string, s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return in
	}
	for _, x := range in {
		if strings.TrimSpace(x) == s {
			return in
		}
	}
	return append(in, s)
}

func dedupNonEmpty(in []string) []string {
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

func dedupIPs(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		key := ip.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

// NodeIDsSorted returns a stable ordering of node IDs for rendering/navigation.
func (g *TopologyGraph) NodeIDsSorted() []string {
	if g == nil {
		return nil
	}
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// NodeByLabelMatch returns node IDs whose label or discovered names contain the query.
// This is intended for TUI search within the topology view.
func (g *TopologyGraph) NodeByLabelMatch(query string) []string {
	if g == nil {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var out []string
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		lbl := strings.ToLower(strings.TrimSpace(n.Label))
		if strings.Contains(lbl, q) {
			out = append(out, id)
			continue
		}
		for _, dn := range n.DiscoveredNames {
			if strings.Contains(strings.ToLower(strings.TrimSpace(dn)), q) {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
