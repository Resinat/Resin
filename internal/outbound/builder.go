package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	sbOutbound "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	sJson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// OutboundBuilder creates outbound instances from raw node options.
type OutboundBuilder interface {
	Build(rawOptions json.RawMessage) (adapter.Outbound, error)
}

// ---------------------------------------------------------------------------
// SingboxBuilder — creates real sing-box adapter.Outbound instances.
// ---------------------------------------------------------------------------

// SingboxBuilder builds real sing-box outbound instances from raw JSON options.
// It holds a fully-wired context with DNS services so that domain-based
// outbound servers can be resolved.
type SingboxBuilder struct {
	registry            *sbOutbound.Registry
	ctx                 context.Context
	logFactory          log.Factory
	dnsTransportManager *dns.TransportManager
	dnsRouter           *dns.Router
}

// NewSingboxBuilder creates a SingboxBuilder with a complete sing-box service
// graph (registries + DNS). The caller must call Close() when done.
func NewSingboxBuilder() (*SingboxBuilder, error) {
	ctx := context.Background()
	ctx = include.Context(ctx) // inject protocol registries

	logFactory := log.NewNOPFactory()
	logger := logFactory.NewLogger("resin-outbound")

	// --- Service graph (same order as Demos/simple-proxy/main.go) -----------

	// Endpoint Manager
	endpointMgr := endpoint.NewManager(logger, service.FromContext[adapter.EndpointRegistry](ctx))
	service.MustRegister[adapter.EndpointManager](ctx, endpointMgr)

	// Inbound Manager (required dependency even though unused)
	inboundMgr := inbound.NewManager(logger, service.FromContext[adapter.InboundRegistry](ctx), endpointMgr)
	service.MustRegister[adapter.InboundManager](ctx, inboundMgr)

	// Outbound Manager (sing-box's own manager, for detour resolution)
	outboundMgr := sbOutbound.NewManager(logger, service.FromContext[adapter.OutboundRegistry](ctx), endpointMgr, "")
	service.MustRegister[adapter.OutboundManager](ctx, outboundMgr)

	// DNS Transport Manager
	dnsTransportMgr := dns.NewTransportManager(logger, service.FromContext[adapter.DNSTransportRegistry](ctx), outboundMgr, "")
	service.MustRegister[adapter.DNSTransportManager](ctx, dnsTransportMgr)

	// DNS Router
	dnsRouter := dns.NewRouter(ctx, logFactory, option.DNSOptions{})
	service.MustRegister[adapter.DNSRouter](ctx, dnsRouter)

	// Register local DNS transport
	if err := dnsTransportMgr.Create(ctx, logger, "local", "local", &option.LocalDNSServerOptions{}); err != nil {
		return nil, fmt.Errorf("singbox builder: create local DNS transport: %w", err)
	}

	// Start DNS Transport Manager lifecycle
	if err := dnsTransportMgr.Start(adapter.StartStateInitialize); err != nil {
		return nil, fmt.Errorf("singbox builder: initialize DNS transport manager: %w", err)
	}
	if err := dnsTransportMgr.Start(adapter.StartStateStart); err != nil {
		_ = dnsTransportMgr.Close()
		return nil, fmt.Errorf("singbox builder: start DNS transport manager: %w", err)
	}

	// Start DNS Router lifecycle
	if err := dnsRouter.Initialize(nil); err != nil {
		_ = dnsTransportMgr.Close()
		return nil, fmt.Errorf("singbox builder: initialize DNS router: %w", err)
	}
	if err := dnsRouter.Start(adapter.StartStateStart); err != nil {
		_ = dnsRouter.Close()
		_ = dnsTransportMgr.Close()
		return nil, fmt.Errorf("singbox builder: start DNS router: %w", err)
	}

	registry := service.FromContext[adapter.OutboundRegistry](ctx).(*sbOutbound.Registry)

	return &SingboxBuilder{
		registry:            registry,
		ctx:                 ctx,
		logFactory:          logFactory,
		dnsTransportManager: dnsTransportMgr,
		dnsRouter:           dnsRouter,
	}, nil
}

// Build parses rawOptions (a complete sing-box outbound JSON object with
// type/tag fields) into a real adapter.Outbound and runs it through the
// lifecycle stages.
func (b *SingboxBuilder) Build(rawOptions json.RawMessage) (adapter.Outbound, error) {
	// 1. Parse via official option.Outbound path (strips type/tag, creates
	//    typed options via OutboundOptionsRegistry + badjson.UnmarshallExcluded).
	var outboundConfig option.Outbound
	if err := sJson.UnmarshalContext(b.ctx, rawOptions, &outboundConfig); err != nil {
		return nil, fmt.Errorf("parse outbound options: %w", err)
	}

	// 2. Create the outbound instance via the registry.
	logger := b.logFactory.NewLogger("outbound/" + outboundConfig.Type)
	ob, err := b.registry.CreateOutbound(
		b.ctx,
		nil, // router — not needed for simple dialing
		logger,
		outboundConfig.Tag,
		outboundConfig.Type,
		outboundConfig.Options,
	)
	if err != nil {
		return nil, fmt.Errorf("create outbound [%s]: %w", outboundConfig.Type, err)
	}

	// 3. Run lifecycle start stages. On failure, close and return error.
	for _, stage := range adapter.ListStartStages {
		if err := adapter.LegacyStart(ob, stage); err != nil {
			_ = common.Close(ob)
			return nil, fmt.Errorf("outbound start %s [%s]: %w", stage, outboundConfig.Type, err)
		}
	}

	return ob, nil
}

// Close shuts down the builder's internal DNS services.
func (b *SingboxBuilder) Close() error {
	var errs []error
	if b.dnsRouter != nil {
		errs = append(errs, b.dnsRouter.Close())
	}
	if b.dnsTransportManager != nil {
		errs = append(errs, b.dnsTransportManager.Close())
	}
	return errors.Join(errs...)
}
