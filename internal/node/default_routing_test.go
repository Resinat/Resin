package node

import (
	"regexp"
	"testing"
	"time"
)

func TestMatchTagFilter_DefaultRoutingDisabled(t *testing.T) {
	type holder struct {
		name       string
		enabled    bool
		restricted bool
		tags       []string
	}
	restricted := holder{"Restricted", true, true, []string{"us-fast", "eu-slow"}}
	ordinary := holder{"Ordinary", true, false, []string{"jp"}}
	disabled := holder{"Disabled", false, false, []string{"us-fast"}}
	compile := func(patterns ...string) []*regexp.Regexp {
		var result []*regexp.Regexp
		for _, pattern := range patterns {
			result = append(result, regexp.MustCompile(pattern))
		}
		return result
	}
	for _, tc := range []struct {
		name    string
		holders []holder
		filter  TagFilter
		want    bool
	}{
		{"empty restricted", []holder{restricted}, TagFilter{}, false},
		{"empty ordinary", []holder{ordinary}, TagFilter{}, true},
		{"any holder permits default", []holder{restricted, ordinary}, TagFilter{}, true},
		{"permitting holder first", []holder{ordinary, restricted}, TagFilter{}, true},
		{"disabled holder cannot permit default", []holder{restricted, disabled}, TagFilter{}, false},
		{"missing holder cannot permit default", []holder{restricted, {}}, TagFilter{}, false},
		{"any hit", []holder{restricted}, TagFilter{Any: compile("us")}, true},
		{"subscription name hit", []holder{restricted}, TagFilter{Any: compile("^Restricted/")}, true},
		{"any miss", []holder{restricted}, TagFilter{Any: compile("jp")}, false},
		{"wildcard hit", []holder{restricted}, TagFilter{Any: compile(".*")}, true},
		{"must hit", []holder{restricted}, TagFilter{Must: compile("us", "fast")}, true},
		{"must miss", []holder{restricted}, TagFilter{Must: compile("us", "slow")}, false},
		{"positive rules require same tag", []holder{restricted}, TagFilter{Any: compile("us"), Must: compile("slow")}, false},
		{"exclusion only cannot opt in", []holder{restricted}, TagFilter{MustNot: compile("expired")}, false},
		{"exclusion only ordinary", []holder{ordinary}, TagFilter{MustNot: compile("expired")}, true},
		{"exclusion vetoes explicit match", []holder{restricted}, TagFilter{Any: compile("us"), MustNot: compile("eu")}, false},
		{"exclusion vetoes across holders", []holder{ordinary, restricted}, TagFilter{Any: compile("jp"), MustNot: compile("us")}, false},
		{"restricted tags still excluded without positives", []holder{ordinary, restricted}, TagFilter{MustNot: compile("us")}, false},
		{"ordinary holder does not bypass regex", []holder{restricted, ordinary}, TagFilter{Any: compile("hk")}, false},
		{"disabled tags cannot opt in", []holder{restricted, disabled}, TagFilter{Any: compile("^Disabled/")}, false},
		{"empty any body", []holder{restricted}, TagFilter{Any: compile("")}, false},
		{"empty must body", []holder{restricted}, TagFilter{Must: compile("")}, false},
		{"blank body", []holder{{"Restricted", true, true, []string{" "}}}, TagFilter{Any: compile(" ")}, false},
		{"empty body cannot hide miss", []holder{restricted}, TagFilter{Any: compile("", "jp")}, false},
		{"empty body before explicit match", []holder{restricted}, TagFilter{Any: compile("", "us")}, true},
		{"empty body before later tag match", []holder{restricted}, TagFilter{Any: compile("", "eu")}, true},
		{"empty any with explicit must", []holder{restricted}, TagFilter{Any: compile(""), Must: compile("us")}, true},
		{"empty must with explicit any", []holder{restricted}, TagFilter{Any: compile("us"), Must: compile("")}, true},
		{"empty body preserves ordinary behavior", []holder{ordinary}, TagFilter{Any: compile("", "us")}, true},
		{"empty body with later default holder", []holder{restricted, ordinary}, TagFilter{Any: compile("", "hk")}, true},
		{"empty body with earlier default holder", []holder{ordinary, restricted}, TagFilter{Any: compile("", "hk")}, true},
		{"empty body does not bypass exclusion", []holder{ordinary, restricted}, TagFilter{Any: compile(""), MustNot: compile("us")}, false},
		{"explicit match without tags fails", []holder{{"Restricted", true, true, nil}}, TagFilter{Any: compile(".*")}, false},
		{"ordinary holder without tags", []holder{{"Ordinary", true, false, nil}}, TagFilter{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
			holders := make(map[string]holder)
			for _, h := range tc.holders {
				e.AddSubscriptionID(h.name)
				if h.name != "" {
					holders[h.name] = h
				}
			}
			lookup := func(id string, _ Hash) (string, bool, []string, bool, bool) {
				h, ok := holders[id]
				return h.name, h.enabled, h.tags, h.restricted, ok
			}
			if got := e.MatchTagFilter(tc.filter, lookup); got != tc.want {
				t.Fatalf("MatchTagFilter = %v, want %v", got, tc.want)
			}
			if e.IsDisabledBySubscriptions(lookup) {
				t.Fatal("default routing restriction must not disable the node")
			}
		})
	}
}

// An ordinary subscription must stop at the first ANY match even if that rule
// is empty. Increasing the number of later misses should not increase its cost.
func BenchmarkMatchTagFilter_DefaultRoutingShortCircuit(b *testing.B) {
	entry := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	entry.AddSubscriptionID("ordinary")
	tags := []string{"us-node"}
	lookup := func(string, Hash) (string, bool, []string, bool, bool) {
		return "Ordinary", true, tags, false, true
	}
	empty := regexp.MustCompile("")
	miss := regexp.MustCompile("^will-never-match$")
	for _, tc := range []struct {
		name  string
		count int
	}{{"10", 10}, {"100", 100}, {"1000", 1000}} {
		b.Run(tc.name, func(b *testing.B) {
			filter := TagFilter{Any: []*regexp.Regexp{empty}}
			for range tc.count {
				filter.Any = append(filter.Any, miss)
			}
			b.ResetTimer()
			for range b.N {
				if !entry.MatchTagFilter(filter, lookup) {
					b.Fatal("ordinary subscription should match the first ANY rule")
				}
			}
		})
	}
}
