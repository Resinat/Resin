package proxy

import "testing"

func TestParseV1PlatformAccountIdentity(t *testing.T) {
	tests := []struct {
		identity string
		token    string
		plat     string
		account  string
	}{
		{identity: "", token: "", plat: "", account: ""},
		{identity: ":", token: "", plat: "", account: ""},
		{identity: ".", token: "", plat: "", account: ""},
		{identity: "a", token: "", plat: "a", account: ""},
		{identity: "a:", token: "", plat: "a", account: ""},
		{identity: ":b", token: "", plat: "", account: "b"},
		{identity: ".:", token: "", plat: "", account: ":"},
		{identity: ":.", token: "", plat: "", account: "."},
		{identity: ":..", token: "", plat: "", account: ".."},
		{identity: "a:b", token: "", plat: "a", account: "b"},
	}
	for _, tt := range tests {
		t.Run(tt.identity, func(t *testing.T) {
			plat, account := parseV1PlatformAccountIdentity(tt.identity)
			if plat != tt.plat || account != tt.account {
				t.Fatalf(
					"identity=%q: got plat=%q account=%q, want plat=%q account=%q",
					tt.identity,
					plat,
					account,
					tt.plat,
					tt.account,
				)
			}
		})
	}
}

func TestParseForwardCredentialV1(t *testing.T) {
	tests := []struct {
		credential string
		token      string
		plat       string
		account    string
	}{
		{credential: "", token: "", plat: "", account: ""},
		{credential: "a", token: "", plat: "a", account: ""},
		{credential: ".", token: "", plat: "", account: ""},
		{credential: ":", token: "", plat: "", account: ""},
		{credential: ":.", token: ".", plat: "", account: ""},
		{credential: ".:", token: "", plat: "", account: ""},
		{credential: ".b:", token: "", plat: "", account: "b"},
		{credential: ".:c", token: "c", plat: "", account: ""},
		{credential: ".::c", token: "c", plat: "", account: ":"},
		{credential: "..:", token: "", plat: "", account: "."},
		{credential: "a.b:c", token: "c", plat: "a", account: "b"},
	}
	for _, tt := range tests {
		t.Run(tt.credential, func(t *testing.T) {
			token, plat, account := parseForwardCredentialV1(tt.credential)
			if token != tt.token || plat != tt.plat || account != tt.account {
				t.Fatalf(
					"credential=%q: got token=%q plat=%q account=%q, want token=%q plat=%q account=%q",
					tt.credential,
					token,
					plat,
					account,
					tt.token,
					tt.plat,
					tt.account,
				)
			}
		})
	}
}

func TestParseForwardCredentialV1WhenAuthDisabled(t *testing.T) {
	tests := []struct {
		credential string
		plat       string
		account    string
	}{
		{credential: "plat:acct", plat: "plat", account: "acct"},
		{credential: "first:second:ignored-token", plat: "first", account: "second"},
		{credential: "my-platform.account-a:any-token", plat: "my-platform", account: "account-a"},
		{credential: ".:c", plat: "", account: ""},
	}
	for _, tt := range tests {
		t.Run(tt.credential, func(t *testing.T) {
			plat, account := parseForwardCredentialV1WhenAuthDisabled(tt.credential)
			if plat != tt.plat || account != tt.account {
				t.Fatalf(
					"credential=%q: got plat=%q account=%q, want plat=%q account=%q",
					tt.credential,
					plat,
					account,
					tt.plat,
					tt.account,
				)
			}
		})
	}
}
