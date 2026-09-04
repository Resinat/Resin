package service

import (
	"sort"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

// ------------------------------------------------------------------
// Leases
// ------------------------------------------------------------------

// LeaseResponse is the API response for a lease.
type LeaseResponse struct {
	PlatformID         string   `json:"platform_id"`
	Account            string   `json:"account"`
	NodeHash           string   `json:"node_hash"`
	NodeTag            string   `json:"node_tag"`
	EgressIP           string   `json:"egress_ip"`
	ReferenceLatencyMs *float64 `json:"reference_latency_ms,omitempty"`
	CreatedAt          string   `json:"created_at"`
	Expiry             string   `json:"expiry"`
	LastAccessed       string   `json:"last_accessed"`
}

func leaseToResponse(lease model.Lease, nodeTag string, referenceLatencyMs *float64) LeaseResponse {
	return LeaseResponse{
		PlatformID:         lease.PlatformID,
		Account:            lease.Account,
		NodeHash:           lease.NodeHash,
		NodeTag:            nodeTag,
		EgressIP:           lease.EgressIP,
		ReferenceLatencyMs: referenceLatencyMs,
		CreatedAt:          time.Unix(0, lease.CreatedAtNs).UTC().Format(time.RFC3339Nano),
		Expiry:             time.Unix(0, lease.ExpiryNs).UTC().Format(time.RFC3339Nano),
		LastAccessed:       time.Unix(0, lease.LastAccessedNs).UTC().Format(time.RFC3339Nano),
	}
}

func (s *ControlPlaneService) resolveLeaseNodeTag(hash node.Hash) string {
	if s == nil || s.Pool == nil {
		return ""
	}
	return s.Pool.ResolveNodeDisplayTag(hash)
}

func (s *ControlPlaneService) resolveLeaseNodeTagFromHex(hashHex string) string {
	hash, err := node.ParseHex(hashHex)
	if err != nil {
		return ""
	}
	return s.resolveLeaseNodeTag(hash)
}

func (s *ControlPlaneService) resolveLeaseNodeReferenceLatency(hash node.Hash) *float64 {
	if s == nil || s.Pool == nil || s.RuntimeCfg == nil {
		return nil
	}
	entry, ok := s.Pool.GetEntry(hash)
	if !ok {
		return nil
	}
	cfg := s.RuntimeCfg.Load()
	if cfg == nil {
		return nil
	}
	avgMs, ok := node.AverageEWMAForDomainsMs(entry, cfg.LatencyAuthorities)
	if !ok {
		return nil
	}
	return &avgMs
}

func (s *ControlPlaneService) resolveLeaseNodeReferenceLatencyFromHex(hashHex string) *float64 {
	hash, err := node.ParseHex(hashHex)
	if err != nil {
		return nil
	}
	return s.resolveLeaseNodeReferenceLatency(hash)
}

// ListLeases returns all leases for a platform.
func (s *ControlPlaneService) ListLeases(platformID string) ([]LeaseResponse, error) {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return nil, notFound("platform not found")
	}
	var result []LeaseResponse
	s.Router.RangeLeases(platformID, func(account string, lease routing.Lease) bool {
		result = append(result, leaseToResponse(model.Lease{
			PlatformID:     platformID,
			Account:        account,
			NodeHash:       lease.NodeHash.Hex(),
			EgressIP:       lease.EgressIP.String(),
			CreatedAtNs:    lease.CreatedAtNs,
			ExpiryNs:       lease.ExpiryNs,
			LastAccessedNs: lease.LastAccessedNs,
		}, s.resolveLeaseNodeTag(lease.NodeHash), s.resolveLeaseNodeReferenceLatency(lease.NodeHash)))
		return true
	})
	if result == nil {
		result = []LeaseResponse{}
	}
	return result, nil
}

// GetLease returns a single lease.
func (s *ControlPlaneService) GetLease(platformID, account string) (*LeaseResponse, error) {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return nil, notFound("platform not found")
	}
	ml := s.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: account})
	if ml == nil {
		return nil, notFound("lease not found")
	}
	resp := leaseToResponse(*ml, s.resolveLeaseNodeTagFromHex(ml.NodeHash), s.resolveLeaseNodeReferenceLatencyFromHex(ml.NodeHash))
	return &resp, nil
}

// InheritLeaseByPlatformName copies a valid parent lease onto newAccount.
func (s *ControlPlaneService) InheritLeaseByPlatformName(platformName, parentAccount, newAccount string) error {
	platformName = strings.TrimSpace(platformName)
	if platformName == "" {
		return invalidArg("platform: must be non-empty")
	}
	parentAccount = strings.TrimSpace(parentAccount)
	if parentAccount == "" {
		return invalidArg("parent_account: must be non-empty")
	}
	newAccount = strings.TrimSpace(newAccount)
	if newAccount == "" {
		return invalidArg("new_account: must be non-empty")
	}
	if parentAccount == newAccount {
		return invalidArg("new_account: must differ from parent_account")
	}

	plat, ok := s.Pool.GetPlatformByName(platformName)
	if !ok || plat == nil {
		return notFound("platform not found")
	}

	parentLease := s.Router.ReadLease(model.LeaseKey{
		PlatformID: plat.ID,
		Account:    parentAccount,
	})
	nowNs := time.Now().UnixNano()
	if parentLease == nil || parentLease.ExpiryNs < nowNs {
		return notFound("parent lease not found")
	}

	next := *parentLease
	next.Account = newAccount
	if err := s.Router.UpsertLease(next); err != nil {
		return internal("inherit lease", err)
	}

	return nil
}

// DeleteLease removes a single lease.
func (s *ControlPlaneService) DeleteLease(platformID, account string) error {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return notFound("platform not found")
	}
	if !s.Router.DeleteLease(platformID, account) {
		return notFound("lease not found")
	}
	return nil
}

// DeleteAllLeases removes all leases for a platform.
func (s *ControlPlaneService) DeleteAllLeases(platformID string) error {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return notFound("platform not found")
	}
	s.Router.DeleteAllLeases(platformID)
	return nil
}

// BindLease binds (or rebinds) an account to a specific node on the given platform.
// The node must be routable on the platform.
func (s *ControlPlaneService) BindLease(platformID, account, nodeHashHex string) (*LeaseResponse, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, invalidArg("account: must be non-empty")
	}
	nodeHashHex = strings.TrimSpace(nodeHashHex)
	h, err := node.ParseHex(nodeHashHex)
	if err != nil {
		return nil, invalidArg("node_hash: invalid format")
	}

	plat, ok := s.Pool.GetPlatform(platformID)
	if !ok {
		return nil, notFound("platform not found")
	}

	if !plat.View().Contains(h) {
		return nil, notFound("node is not routable on this platform")
	}

	entry, ok := s.Pool.GetEntry(h)
	if !ok {
		return nil, notFound("node not found")
	}
	egressIP := entry.GetEgressIP()
	if !egressIP.IsValid() {
		return nil, invalidArg("node has no egress IP")
	}

	nowNs := time.Now().UnixNano()
	ttlNs := plat.StickyTTLNs
	if ttlNs <= 0 {
		ttlNs = int64(24 * time.Hour) // default 24h
	}

	ml := model.Lease{
		PlatformID:     platformID,
		Account:        account,
		NodeHash:       h.Hex(),
		EgressIP:       egressIP.String(),
		CreatedAtNs:    nowNs,
		ExpiryNs:       nowNs + ttlNs,
		LastAccessedNs: nowNs,
	}
	if err := s.Router.UpsertLease(ml); err != nil {
		return nil, internal("bind lease", err)
	}

	resp := leaseToResponse(ml, s.resolveLeaseNodeTag(h), s.resolveLeaseNodeReferenceLatency(h))
	return &resp, nil
}

// IPLoadEntry is the API response for IP load stats.
type IPLoadEntry struct {
	EgressIP   string `json:"egress_ip"`
	LeaseCount int64  `json:"lease_count"`
}

// NodeLeaseResponse is the API response for a lease scoped to a specific node.
// Unlike LeaseResponse, it carries the owning platform so the caller can render
// cross-platform lease bindings for a single node.
type NodeLeaseResponse struct {
	PlatformID   string `json:"platform_id"`
	PlatformName string `json:"platform_name"`
	Account      string `json:"account"`
	NodeHash     string `json:"node_hash"`
	EgressIP     string `json:"egress_ip"`
	CreatedAt    string `json:"created_at"`
	Expiry       string `json:"expiry"`
	LastAccessed string `json:"last_accessed"`
}

// ListLeasesByNode returns every lease bound to the given node hash.
// When platformID is non-empty, only leases under that platform are returned;
// otherwise leases across all platforms are aggregated.
// Results are sorted by CreatedAtNs descending (newest first).
func (s *ControlPlaneService) ListLeasesByNode(nodeHashHex, platformID string) ([]NodeLeaseResponse, error) {
	nodeHashHex = strings.TrimSpace(nodeHashHex)
	h, err := node.ParseHex(nodeHashHex)
	if err != nil {
		return nil, invalidArg("node_hash: invalid format")
	}
	if _, ok := s.Pool.GetEntry(h); !ok {
		return nil, notFound("node not found")
	}

	type entry struct {
		resp        NodeLeaseResponse
		createdAtNs int64
	}
	platformNameCache := make(map[string]string)
	resolvePlatformName := func(pid string) string {
		if name, ok := platformNameCache[pid]; ok {
			return name
		}
		name := ""
		if plat, ok := s.Pool.GetPlatform(pid); ok {
			name = plat.Name
		}
		platformNameCache[pid] = name
		return name
	}

	var entries []entry
	addLease := func(pid, account string, lease routing.Lease) {
		if lease.NodeHash != h {
			return
		}
		entries = append(entries, entry{
			resp: NodeLeaseResponse{
				PlatformID:   pid,
				PlatformName: resolvePlatformName(pid),
				Account:      account,
				NodeHash:     lease.NodeHash.Hex(),
				EgressIP:     lease.EgressIP.String(),
				CreatedAt:    time.Unix(0, lease.CreatedAtNs).UTC().Format(time.RFC3339Nano),
				Expiry:       time.Unix(0, lease.ExpiryNs).UTC().Format(time.RFC3339Nano),
				LastAccessed: time.Unix(0, lease.LastAccessedNs).UTC().Format(time.RFC3339Nano),
			},
			createdAtNs: lease.CreatedAtNs,
		})
	}

	platformID = strings.TrimSpace(platformID)
	if platformID != "" {
		if _, ok := s.Pool.GetPlatform(platformID); !ok {
			return nil, notFound("platform not found")
		}
		s.Router.RangeLeases(platformID, func(account string, lease routing.Lease) bool {
			addLease(platformID, account, lease)
			return true
		})
	} else {
		s.Router.RangeAllLeases(func(pid, account string, lease routing.Lease) bool {
			addLease(pid, account, lease)
			return true
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].createdAtNs != entries[j].createdAtNs {
			return entries[i].createdAtNs > entries[j].createdAtNs
		}
		if entries[i].resp.PlatformName != entries[j].resp.PlatformName {
			return entries[i].resp.PlatformName < entries[j].resp.PlatformName
		}
		return entries[i].resp.Account < entries[j].resp.Account
	})

	result := make([]NodeLeaseResponse, 0, len(entries))
	for _, e := range entries {
		result = append(result, e.resp)
	}
	return result, nil
}

// GetIPLoad returns IP load stats for a platform.
func (s *ControlPlaneService) GetIPLoad(platformID string) ([]IPLoadEntry, error) {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return nil, notFound("platform not found")
	}
	snapshot := s.Router.SnapshotIPLoad(platformID)
	result := make([]IPLoadEntry, 0, len(snapshot))
	for ip, count := range snapshot {
		result = append(result, IPLoadEntry{
			EgressIP:   ip.String(),
			LeaseCount: count,
		})
	}
	return result, nil
}
