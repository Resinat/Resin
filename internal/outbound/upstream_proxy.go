package outbound

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
)

const (
	// upstreamProxyOutboundTag is the sing-box outbound tag every node
	// outbound detours through when RESIN_UPSTREAM_PROXY is configured.
	upstreamProxyOutboundTag = "__resin_upstream_proxy"

	// upstreamProxyLocalDNSTransportTag is a dedicated "local" DNS transport
	// used to resolve the upstream proxy server hostname via the system
	// resolver (docker DNS / /etc/hosts) when it is a domain name.
	upstreamProxyLocalDNSTransportTag = "resin-upstream-proxy-local-dns"
)

// upstreamProxySpec is a parsed RESIN_UPSTREAM_PROXY value ready to be turned
// into a sing-box outbound.
type upstreamProxySpec struct {
	outboundType   string // "socks" or "http"
	options        any    // *option.SOCKSOutboundOptions or *option.HTTPOutboundOptions
	serverIsDomain bool
}

// parseUpstreamProxyURL parses a proxy URI of the form
//
//	socks5://[user:pass@]host:port
//	http://[user:pass@]host:port
//	https://[user:pass@]host:port
//	host:port  (scheme-less, treated as http)
//
// Default ports: socks 1080, http 80, https 443.
func parseUpstreamProxyURL(raw string) (upstreamProxySpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return upstreamProxySpec{}, fmt.Errorf("empty upstream proxy URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return upstreamProxySpec{}, fmt.Errorf("parse upstream proxy URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "socks", "socks5", "socks5h", "http", "https":
	default:
		return upstreamProxySpec{}, fmt.Errorf(
			"unsupported upstream proxy scheme %q (supported: socks5://, http://, https://)",
			scheme,
		)
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return upstreamProxySpec{}, fmt.Errorf("upstream proxy URL: missing host")
	}
	port, err := upstreamProxyPort(u, scheme)
	if err != nil {
		return upstreamProxySpec{}, err
	}
	_, parseErr := netip.ParseAddr(host)
	hostIsIP := parseErr == nil
	var username, password string
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	serverOptions := option.ServerOptions{
		Server:     host,
		ServerPort: uint16(port),
	}
	switch scheme {
	case "socks", "socks5", "socks5h":
		return upstreamProxySpec{
			outboundType:   C.TypeSOCKS,
			serverIsDomain: !hostIsIP,
			options: &option.SOCKSOutboundOptions{
				ServerOptions: serverOptions,
				Version:       "5",
				Username:      username,
				Password:      password,
			},
		}, nil
	default: // http, https
		opts := &option.HTTPOutboundOptions{
			ServerOptions: serverOptions,
			Username:      username,
			Password:      password,
		}
		if scheme == "https" {
			opts.OutboundTLSOptionsContainer.TLS = &option.OutboundTLSOptions{
				ServerName: host,
			}
		}
		return upstreamProxySpec{
			outboundType:   C.TypeHTTP,
			serverIsDomain: !hostIsIP,
			options:        opts,
		}, nil
	}
}

func upstreamProxyPort(u *url.URL, scheme string) (int, error) {
	if rawPort := u.Port(); rawPort != "" {
		p, err := strconv.Atoi(rawPort)
		if err != nil || p < 1 || p > 65535 {
			return 0, fmt.Errorf("upstream proxy URL: invalid port %q", rawPort)
		}
		return p, nil
	}
	switch scheme {
	case "socks", "socks5", "socks5h":
		return 1080, nil
	case "https":
		return 443, nil
	default:
		return 80, nil
	}
}

// createUpstreamProxy builds the dedicated upstream outbound and registers it
// in the given outbound manager so that node outbounds can detour through it.
// When the proxy server is a domain name, a dedicated system-resolver DNS
// transport is registered so the hostname resolves inside the container.
func createUpstreamProxy(
	ctx context.Context,
	outboundMgr adapter.OutboundManager,
	dnsTransportMgr adapter.DNSTransportManager,
	logger log.ContextLogger,
	raw string,
) (adapter.Outbound, error) {
	spec, err := parseUpstreamProxyURL(raw)
	if err != nil {
		return nil, fmt.Errorf("upstream proxy: %w", err)
	}
	if spec.serverIsDomain {
		if err := dnsTransportMgr.Create(
			ctx,
			logger,
			upstreamProxyLocalDNSTransportTag,
			C.DNSTypeLocal,
			&option.LocalDNSServerOptions{},
		); err != nil {
			return nil, fmt.Errorf("upstream proxy: register local DNS resolver: %w", err)
		}
		setUpstreamProxyDomainResolver(spec.options, upstreamProxyLocalDNSTransportTag)
	}
	if err := outboundMgr.Create(
		ctx,
		nil, // router - dialing does not require one
		logger,
		upstreamProxyOutboundTag,
		spec.outboundType,
		spec.options,
	); err != nil {
		return nil, fmt.Errorf("upstream proxy: create outbound: %w", err)
	}
	ob, loaded := outboundMgr.Outbound(upstreamProxyOutboundTag)
	if !loaded || ob == nil {
		return nil, fmt.Errorf("upstream proxy: registered outbound %q not found", upstreamProxyOutboundTag)
	}
	// The outbound manager is never started in this service graph, so run the
	// lifecycle stages manually, matching SingboxBuilder.Build.
	for _, stage := range adapter.ListStartStages {
		if err := adapter.LegacyStart(ob, stage); err != nil {
			_ = common.Close(ob)
			return nil, fmt.Errorf("upstream proxy: start %s: %w", stage, err)
		}
	}
	return ob, nil
}

func setUpstreamProxyDomainResolver(options any, resolverTag string) {
	switch opts := options.(type) {
	case *option.SOCKSOutboundOptions:
		opts.DialerOptions.DomainResolver = &option.DomainResolveOptions{Server: resolverTag}
	case *option.HTTPOutboundOptions:
		opts.DialerOptions.DomainResolver = &option.DomainResolveOptions{Server: resolverTag}
	}
}

// injectUpstreamProxyDetour rewrites an outbound's dialer options so that its
// server connections go through the upstream proxy. An existing detour on the
// node is preserved (node-level detours take precedence).
func injectUpstreamProxyDetour(options any) {
	wrapper, ok := options.(option.DialerOptionsWrapper)
	if !ok {
		return
	}
	dialOptions := wrapper.TakeDialerOptions()
	if dialOptions.Detour != "" {
		return
	}
	dialOptions.Detour = upstreamProxyOutboundTag
	wrapper.ReplaceDialerOptions(dialOptions)
}
