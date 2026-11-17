package outbound

import (
	"reflect"
	"testing"

	"github.com/Resinat/Resin/internal/testutil"
)

func TestRelayProtocolCapabilities(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{`{"type":"tor"}`, []string{"tcp"}},
		{`{"type":"http"}`, []string{"tcp"}},
		{`{"type":"ssh"}`, []string{"tcp"}},
		{`{"type":"shadowtls"}`, nil},
		{`{"type":"socks","version":"4"}`, []string{"tcp"}},
		{`{"type":"socks","version":"4a"}`, []string{"tcp"}},
		{`{"type":"socks","version":"5"}`, []string{"tcp", "udp"}},
		{`{"type":"socks","network":"tcp"}`, []string{"tcp"}},
		{`{"type":"shadowsocks","network":["udp"]}`, []string{"udp"}},
	} {
		if got := relayNetworks([]byte(tc.raw), testutil.NewNoopOutbound()); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got=%v want=%v", tc.raw, got, tc.want)
		}
	}
}
