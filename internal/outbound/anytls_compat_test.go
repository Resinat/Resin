package outbound

import (
	"testing"

	anytlsutil "github.com/anytls/sing-anytls/util"
)

func TestAnyTLSClientMetadataDisabled(t *testing.T) {
	if got := anytlsutil.Verison; got != "" {
		t.Fatalf("AnyTLS client metadata = %q, want empty", got)
	}
}
