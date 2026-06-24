package subscription

import (
	"strconv"
	"strings"
)

const SubscriptionUserinfoHeader = "Subscription-Userinfo"

// ParseSubscriptionUserinfo parses the common subscription usage header:
// upload=...; download=...; total=...; expire=...
func ParseSubscriptionUserinfo(raw string, updatedAtNs int64) (UsageInfo, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return UsageInfo{}, false
	}

	info := UsageInfo{UpdatedAtNs: updatedAtNs}
	found := false
	for _, part := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || n < 0 {
			continue
		}

		switch strings.ToLower(strings.TrimSpace(key)) {
		case "upload":
			info.UploadBytes = n
			found = true
		case "download":
			info.DownloadBytes = n
			found = true
		case "total":
			info.TotalBytes = n
			found = true
		case "expire":
			info.ExpireUnix = n
			found = true
		}
	}
	if !found {
		return UsageInfo{}, false
	}
	return info, true
}
