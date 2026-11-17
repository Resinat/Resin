package routing

import (
	"errors"
	"math/rand/v2"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

var ErrNoAvailableRelay = errors.New("no healthy compatible chain relay")

type RelayPool interface {
	GetEntry(node.Hash) (*node.NodeEntry, bool)
	NodeRegion(node.Hash) string
	RelayView(region, network string) platform.ReadOnlyView
	IsRelayEligible(node.Hash, string) bool
}

type RelaySelector struct {
	pool        RelayPool
	authorities func() []string
	window      func() time.Duration
}

func NewRelaySelector(pool RelayPool, authorities func() []string, window func() time.Duration) *RelaySelector {
	return &RelaySelector{pool: pool, authorities: authorities, window: window}
}

// Pick compares existing authority measurements; it does not create leases or
// attribute a failed end-to-end connection to the chosen relay.
func (s *RelaySelector) Pick(landing node.Hash, network string) (node.Hash, error) {
	rng := randomRouteRNGPool.Get().(*rand.Rand)
	defer randomRouteRNGPool.Put(rng)
	region := s.pool.NodeRegion(landing)
	regions := []string{""}
	if region != "" {
		regions = []string{region, ""}
	}
	for _, region := range regions {
		view := s.pool.RelayView(region, network)
		pick := func(exclude node.Hash) (node.Hash, *node.NodeEntry, bool) {
			for i := 0; i < 8; i++ {
				h, ok := view.RandomPick(rng)
				if !ok {
					break
				}
				if h == landing || h == exclude || !s.pool.IsRelayEligible(h, network) {
					continue
				}
				if e, ok := s.pool.GetEntry(h); ok {
					return h, e, true
				}
			}
			return node.Zero, nil, false
		}
		h1, e1, ok := pick(node.Zero)
		if !ok {
			continue
		}
		h2, e2, ok := pick(h1)
		if !ok {
			return h1, nil
		}
		var authorities []string
		if s.authorities != nil {
			authorities = s.authorities()
		}
		window := time.Minute * 10
		if s.window != nil {
			window = s.window()
		}
		l1, l2, comparable := averageComparableAuthorityLatencies(e1, e2, authorities, time.Now(), window)
		if comparable && l1 < l2 {
			return h1, nil
		}
		return h2, nil
	}
	return node.Zero, ErrNoAvailableRelay
}
