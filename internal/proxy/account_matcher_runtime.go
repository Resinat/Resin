package proxy

import (
	"sync/atomic"

	"github.com/Resinat/Resin/internal/model"
)

// AccountMatcherRuntime stores the current account matcher in an atomic pointer.
// Readers stay lock-free; updates swap in a fully built immutable matcher.
type AccountMatcherRuntime struct {
	ptr atomic.Pointer[AccountMatcher]
}

var _ AccountRuleMatcher = (*AccountMatcherRuntime)(nil)

// NewAccountMatcherRuntime creates a runtime matcher store with an initial matcher.
// If initial is nil, it uses an empty matcher.
func NewAccountMatcherRuntime(initial *AccountMatcher) *AccountMatcherRuntime {
	if initial == nil {
		initial = BuildAccountMatcher(nil)
	}
	r := &AccountMatcherRuntime{}
	r.ptr.Store(initial)
	return r
}

// Match resolves header rules using the current matcher snapshot.
func (r *AccountMatcherRuntime) Match(host, path string) []string {
	if r == nil {
		return nil
	}
	m := r.ptr.Load()
	if m == nil {
		return nil
	}
	return m.Match(host, path)
}

// MatchWithPrefix resolves rules using the current matcher snapshot and returns
// both the matched url_prefix and header list.
func (r *AccountMatcherRuntime) MatchWithPrefix(host, path string) (string, []string) {
	if r == nil {
		return "", nil
	}
	m := r.ptr.Load()
	if m == nil {
		return "", nil
	}
	return m.MatchWithPrefix(host, path)
}

// Swap atomically replaces the current matcher.
// Passing nil resets to an empty matcher.
func (r *AccountMatcherRuntime) Swap(next *AccountMatcher) {
	if r == nil {
		return
	}
	if next == nil {
		next = BuildAccountMatcher(nil)
	}
	r.ptr.Store(next)
}

// ReplaceRules rebuilds a matcher from persisted rules and atomically replaces it.
func (r *AccountMatcherRuntime) ReplaceRules(rules []model.AccountHeaderRule) {
	if r == nil {
		return
	}
	r.ptr.Store(BuildAccountMatcher(rules))
}

// Current returns the currently published matcher pointer.
func (r *AccountMatcherRuntime) Current() *AccountMatcher {
	if r == nil {
		return nil
	}
	return r.ptr.Load()
}
