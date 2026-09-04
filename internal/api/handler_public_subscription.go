package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Resinat/Resin/internal/service"
	"github.com/Resinat/Resin/internal/subscription"
)

// NewPublicSubscriptionHandler returns the unauthenticated handler for
// /sub/{id}/{token}?format=.... An omitted format is auto-detected. Endpoint
// capability checks happen in the endpoint inbound mux before this handler is
// invoked.
func NewPublicSubscriptionHandler(cp *service.ControlPlaneService) http.Handler {
	if cp == nil {
		return http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.Handle("GET /sub/{id}/{token}", HandlePublicSubscription(cp))
	return mux
}

// HandlePublicSubscription serves a subscription in the requested client
// format. The same token is shared by all formats.
func HandlePublicSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		format := strings.TrimSpace(r.URL.Query().Get("format"))
		if format == "" || strings.EqualFold(format, "auto") {
			format = detectPublicSubscriptionFormat(r)
		}

		result, err := cp.RenderPublicSubscription(
			PathParam(r, "id"),
			PathParam(r, "token"),
			format,
		)
		if err != nil {
			writeServiceError(w, err)
			return
		}

		setPublicSubscriptionMetadata(w, cp, PathParam(r, "id"))
		w.Header().Set("Content-Type", result.ContentType)
		w.Header().Set("Cache-Control", "no-cache")
		if result.Skipped > 0 {
			w.Header().Set("X-Resin-Skipped-Nodes", formatInt(result.Skipped))
		}
		if userinfo := subscriptionUserinfoHeader(result.Usage); userinfo != "" {
			w.Header().Set("Subscription-Userinfo", userinfo)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(result.Body)
	}
}

func setPublicSubscriptionMetadata(w http.ResponseWriter, cp *service.ControlPlaneService, id string) {
	name := "remote-file"
	if cp != nil && cp.SubMgr != nil {
		if sub := cp.SubMgr.Lookup(id); sub != nil {
			if value := safeHeaderValue(sub.Name()); value != "" {
				name = value
			}
		}
	}

	w.Header().Set("Profile-Title", name)
	w.Header().Set("Profile-Update-Interval", "24")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
}

func safeHeaderValue(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

func detectPublicSubscriptionFormat(r *http.Request) string {
	userAgent := strings.ToLower(r.UserAgent())
	switch {
	case strings.Contains(userAgent, "v2rayn") || strings.Contains(userAgent, "v2ray n"):
		return service.PublicSubscriptionFormatV2Ray
	case strings.Contains(userAgent, "sing-box") || strings.Contains(userAgent, "singbox"):
		return service.PublicSubscriptionFormatSingBox
	default:
		return service.PublicSubscriptionFormatClash
	}
}

func formatInt(value int) string {
	if value < 0 {
		return "0"
	}
	return strconv.Itoa(value)
}

func subscriptionUserinfoHeader(info subscription.UsageInfo) string {
	return subscription.FormatSubscriptionUserinfo(info)
}
