package outbound

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

const (
	localDNSTransportTag = "local"

	secureDNSFailoverTransportTag  = "resin-secure-dns"
	secureDNSFailoverTransportType = "resin-sequential-failover"
	secureDNSQueryPath             = "/dns-query"

	customDNSTransportTagPrefix = "resin-dns-upstream-"
)

type secureDNSTransportSpec struct {
	tag           string
	transportType string
	options       any
}

type secureDNSFailoverOptions struct {
	Upstreams []string `json:"upstreams,omitempty"`
}

type secureDNSFailoverTransport struct {
	manager      adapter.DNSTransportManager
	tag          string
	upstreamTags []string
}

func registerSecureDNSTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[secureDNSFailoverOptions](registry, secureDNSFailoverTransportType, newSecureDNSFailoverTransport)
}

func secureDNSTransportSpecsForUpstreams(upstreams []string) ([]secureDNSTransportSpec, error) {
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no DNS upstreams configured")
	}
	return customSecureDNSTransportSpecs(upstreams)
}

func customSecureDNSTransportSpecs(upstreams []string) ([]secureDNSTransportSpec, error) {
	specs := make([]secureDNSTransportSpec, 0, len(upstreams)+2)
	upstreamTags := make([]string, 0, len(upstreams))
	localNeeded := false

	for i, raw := range upstreams {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, fmt.Errorf("DNS upstream %d: empty URI", i+1)
		}
		if strings.EqualFold(raw, localDNSTransportTag) {
			localNeeded = true
			upstreamTags = append(upstreamTags, localDNSTransportTag)
			continue
		}

		spec, needsLocal, err := parseCustomDNSUpstream(raw, customDNSUpstreamTransportTag(i))
		if err != nil {
			return nil, fmt.Errorf("DNS upstream %d %q: %w", i+1, raw, err)
		}
		if needsLocal {
			localNeeded = true
		}
		specs = append(specs, spec)
		upstreamTags = append(upstreamTags, spec.tag)
	}
	if len(upstreamTags) == 0 {
		return nil, fmt.Errorf("no DNS upstreams configured")
	}
	if localNeeded {
		specs = append([]secureDNSTransportSpec{localDNSTransportSpec()}, specs...)
	}
	specs = append(specs, secureDNSFailoverTransportSpec(upstreamTags))
	return specs, nil
}

func customDNSUpstreamTransportTag(index int) string {
	return fmt.Sprintf("%s%d", customDNSTransportTagPrefix, index+1)
}

func localDNSTransportSpec() secureDNSTransportSpec {
	return secureDNSTransportSpec{
		tag:           localDNSTransportTag,
		transportType: C.DNSTypeLocal,
		options:       &option.LocalDNSServerOptions{},
	}
}

func secureDNSFailoverTransportSpec(upstreams []string) secureDNSTransportSpec {
	return secureDNSTransportSpec{
		tag:           secureDNSFailoverTransportTag,
		transportType: secureDNSFailoverTransportType,
		options: &secureDNSFailoverOptions{
			Upstreams: append([]string(nil), upstreams...),
		},
	}
}

func parseCustomDNSUpstream(raw string, tag string) (secureDNSTransportSpec, bool, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return secureDNSTransportSpec{}, false, err
	}
	if u.Scheme == "" {
		return secureDNSTransportSpec{}, false, fmt.Errorf("missing scheme")
	}
	if u.User != nil {
		return secureDNSTransportSpec{}, false, fmt.Errorf("userinfo is not supported")
	}
	if u.Fragment != "" {
		return secureDNSTransportSpec{}, false, fmt.Errorf("fragment is not supported")
	}

	scheme := strings.ToLower(u.Scheme)
	host, port, err := parseDNSUpstreamHostPort(u)
	if err != nil {
		return secureDNSTransportSpec{}, false, err
	}

	query := u.Query()
	if err := validateDNSUpstreamQuery(query); err != nil {
		return secureDNSTransportSpec{}, false, err
	}
	sni := firstNonEmptyQuery(query, "sni", "servername", "server_name")
	bootstrap := strings.TrimSpace(query.Get("bootstrap"))
	if bootstrap != "" && !strings.EqualFold(bootstrap, localDNSTransportTag) {
		return secureDNSTransportSpec{}, false, fmt.Errorf("unsupported bootstrap %q", bootstrap)
	}

	needsLocalResolver := strings.EqualFold(bootstrap, localDNSTransportTag) || dnsUpstreamHostNeedsBootstrap(host)
	remoteOptions := option.RemoteDNSServerOptions{
		LocalDNSServerOptions: option.LocalDNSServerOptions{
			DialerOptions: option.DialerOptions{},
		},
		DNSServerAddressOptions: option.DNSServerAddressOptions{
			Server:     host,
			ServerPort: port,
		},
	}
	if needsLocalResolver {
		remoteOptions.LocalDNSServerOptions.DialerOptions.DomainResolver = &option.DomainResolveOptions{
			Server: localDNSTransportTag,
		}
	}

	switch scheme {
	case C.DNSTypeUDP:
		if sni != "" {
			return secureDNSTransportSpec{}, false, fmt.Errorf("sni is only supported for TLS DNS transports")
		}
		if u.Path != "" {
			return secureDNSTransportSpec{}, false, fmt.Errorf("path is not supported for udp DNS upstreams")
		}
		return secureDNSTransportSpec{tag: tag, transportType: C.DNSTypeUDP, options: &remoteOptions}, needsLocalResolver, nil
	case C.DNSTypeTCP:
		if sni != "" {
			return secureDNSTransportSpec{}, false, fmt.Errorf("sni is only supported for TLS DNS transports")
		}
		if u.Path != "" {
			return secureDNSTransportSpec{}, false, fmt.Errorf("path is not supported for tcp DNS upstreams")
		}
		return secureDNSTransportSpec{tag: tag, transportType: C.DNSTypeTCP, options: &remoteOptions}, needsLocalResolver, nil
	case C.DNSTypeTLS, C.DNSTypeQUIC:
		if u.Path != "" {
			return secureDNSTransportSpec{}, false, fmt.Errorf("path is not supported for %s DNS upstreams", scheme)
		}
		return secureDNSTransportSpec{
			tag:           tag,
			transportType: scheme,
			options:       remoteTLSDNSOptions(remoteOptions, sni),
		}, needsLocalResolver, nil
	case C.DNSTypeHTTPS, C.DNSTypeHTTP3:
		path := u.Path
		if path == "" {
			path = secureDNSQueryPath
		}
		return secureDNSTransportSpec{
			tag:           tag,
			transportType: scheme,
			options: &option.RemoteHTTPSDNSServerOptions{
				RemoteTLSDNSServerOptions: *remoteTLSDNSOptions(remoteOptions, sni),
				Path:                      path,
			},
		}, needsLocalResolver, nil
	default:
		return secureDNSTransportSpec{}, false, fmt.Errorf("unsupported DNS upstream scheme %q", scheme)
	}
}

func parseDNSUpstreamHostPort(u *url.URL) (string, uint16, error) {
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return "", 0, fmt.Errorf("missing host")
	}
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return "", 0, fmt.Errorf("IPv6 addresses must use [addr] URI syntax")
	}
	portRaw := strings.TrimSpace(u.Port())
	if portRaw == "" {
		if hasInvalidDNSUpstreamPortSyntax(u.Host) {
			return "", 0, fmt.Errorf("invalid port")
		}
		return host, 0, nil
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port %q", portRaw)
	}
	return host, uint16(port), nil
}

func hasInvalidDNSUpstreamPortSyntax(hostport string) bool {
	if strings.HasPrefix(hostport, "[") {
		closing := strings.LastIndex(hostport, "]")
		return closing >= 0 && len(hostport) > closing+1 && strings.HasPrefix(hostport[closing+1:], ":")
	}
	lastColon := strings.LastIndex(hostport, ":")
	if lastColon < 0 {
		return false
	}
	return true
}

func validateDNSUpstreamQuery(query url.Values) error {
	allowed := map[string]struct{}{
		"bootstrap":   {},
		"sni":         {},
		"servername":  {},
		"server_name": {},
	}
	for key := range query {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unsupported query parameter %q", key)
		}
	}
	return nil
}

func firstNonEmptyQuery(query url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func remoteTLSDNSOptions(remoteOptions option.RemoteDNSServerOptions, sni string) *option.RemoteTLSDNSServerOptions {
	options := &option.RemoteTLSDNSServerOptions{
		RemoteDNSServerOptions: remoteOptions,
	}
	if sni != "" {
		options.OutboundTLSOptionsContainer.TLS = &option.OutboundTLSOptions{
			ServerName: sni,
		}
	}
	return options
}

func dnsUpstreamHostNeedsBootstrap(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	return true
}

func newSecureDNSFailoverTransport(
	ctx context.Context,
	_ log.ContextLogger,
	tag string,
	options secureDNSFailoverOptions,
) (adapter.DNSTransport, error) {
	manager := service.FromContext[adapter.DNSTransportManager](ctx)
	if manager == nil {
		return nil, fmt.Errorf("secure dns transport: missing DNS transport manager")
	}
	if len(options.Upstreams) == 0 {
		return nil, fmt.Errorf("secure dns transport: no upstreams configured")
	}
	return &secureDNSFailoverTransport{
		manager:      manager,
		tag:          tag,
		upstreamTags: append([]string(nil), options.Upstreams...),
	}, nil
}

func (t *secureDNSFailoverTransport) Type() string {
	return secureDNSFailoverTransportType
}

func (t *secureDNSFailoverTransport) Tag() string {
	return t.tag
}

func (t *secureDNSFailoverTransport) Dependencies() []string {
	return append([]string(nil), t.upstreamTags...)
}

func (t *secureDNSFailoverTransport) Start(stage adapter.StartStage) error {
	return nil
}

func (t *secureDNSFailoverTransport) Close() error {
	return nil
}

func (t *secureDNSFailoverTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	if len(t.upstreamTags) == 0 {
		return nil, fmt.Errorf("secure dns transport: no upstreams configured")
	}

	queryName := "<empty query>"
	if len(message.Question) > 0 {
		queryName = message.Question[0].Name
	}

	var attemptErrs []error
	for _, upstreamTag := range t.upstreamTags {
		upstream, ok := t.manager.Transport(upstreamTag)
		if !ok || upstream == nil {
			attemptErrs = append(attemptErrs, fmt.Errorf("%s: transport not found", upstreamTag))
			continue
		}

		response, err := upstream.Exchange(ctx, message.Copy())
		if err == nil && shouldAcceptSecureDNSResponse(response) {
			return response, nil
		}
		if err == nil {
			if response == nil {
				err = errors.New("empty response")
			} else {
				err = dns.RcodeError(response.Rcode)
			}
		}
		attemptErrs = append(attemptErrs, fmt.Errorf("%s: %w", upstreamTag, err))
	}

	return nil, fmt.Errorf("secure DNS exchange failed for %s: %w", queryName, errors.Join(attemptErrs...))
}

func shouldAcceptSecureDNSResponse(response *mDNS.Msg) bool {
	if response == nil {
		return false
	}
	return response.Rcode == mDNS.RcodeSuccess
}
