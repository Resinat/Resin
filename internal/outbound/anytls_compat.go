package outbound

import anytlsutil "github.com/anytls/sing-anytls/util"

// sing-box v1.12.21 pins sing-anytls v0.0.11, which unconditionally puts
// "sing-anytls/<version>" in the AnyTLS settings frame. Some providers use
// that field to identify and reject clients using the official library
// (Mihomo leaves it empty). The field is not used for protocol negotiation,
// so Resin deliberately disables it before any outbound session can be
// created. Remove this shim when the sing-box dependency is upgraded to a
// version that defaults client metadata to an empty string.
func init() {
	// Verison is the historical exported spelling in sing-anytls v0.0.11.
	anytlsutil.Verison = ""
}
