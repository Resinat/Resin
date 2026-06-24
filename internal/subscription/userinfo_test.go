package subscription

import "testing"

func TestParseSubscriptionUserinfo(t *testing.T) {
	info, ok := ParseSubscriptionUserinfo("upload=1024; download=2048; total=4096; expire=1893456000", 123)
	if !ok {
		t.Fatal("expected userinfo to parse")
	}
	if info.UploadBytes != 1024 || info.DownloadBytes != 2048 || info.TotalBytes != 4096 || info.ExpireUnix != 1893456000 || info.UpdatedAtNs != 123 {
		t.Fatalf("unexpected usage info: %+v", info)
	}
}

func TestParseSubscriptionUserinfo_IgnoresInvalidFields(t *testing.T) {
	info, ok := ParseSubscriptionUserinfo("upload=bad; download=8; total=-1; expire=bad", 456)
	if !ok {
		t.Fatal("expected valid fields to parse")
	}
	if info.UploadBytes != 0 || info.DownloadBytes != 8 || info.TotalBytes != 0 || info.ExpireUnix != 0 || info.UpdatedAtNs != 456 {
		t.Fatalf("unexpected usage info: %+v", info)
	}
}

func TestParseSubscriptionUserinfo_Empty(t *testing.T) {
	if info, ok := ParseSubscriptionUserinfo("", 123); ok || info != (UsageInfo{}) {
		t.Fatalf("empty userinfo parsed: ok=%v info=%+v", ok, info)
	}
}
