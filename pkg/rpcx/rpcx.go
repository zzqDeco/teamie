// Package rpcx provides shared CloudWeGo Kitex client and server option builders
// with ECSZap observability, bounded timeouts, graceful drain signaling, and
// lossless dual-slot (A/B) discovery and dial-time failover.
package rpcx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/connpool"
	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/loadbalance"
	"github.com/cloudwego/kitex/pkg/loadbalance/lbcache"
	"github.com/cloudwego/kitex/pkg/remote"
	"github.com/cloudwego/kitex/pkg/remote/codec"
	rconnpool "github.com/cloudwego/kitex/pkg/remote/connpool"
	"github.com/cloudwego/kitex/pkg/remote/trans/gonet"
	rtransmeta "github.com/cloudwego/kitex/pkg/remote/transmeta"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	"teamie/pkg/observability"
)

const (
	defaultRefreshInterval = 250 * time.Millisecond
	defaultExpireInterval  = 15 * time.Second
	defaultDrainingTTL     = 5 * time.Second
	defaultUnreachableTTL  = 120 * time.Millisecond
	dnsLookupTimeout       = 400 * time.Millisecond
)

var resolverSeq atomic.Uint64

// DrainState tracks whether a Kitex server has entered its pre-stop graceful drain
// window. While draining, the server keeps serving RPCs cleanly and sets the TTHeader
// crrst flag on responses so clients immediately retire pooled connections to this slot.
type DrainState struct {
	draining atomic.Bool
}

// NewDrainState creates a new server DrainState.
func NewDrainState() *DrainState {
	return &DrainState{}
}

// MarkDraining marks the RPC server as draining so subsequent responses carry crrst=1.
func (d *DrainState) MarkDraining() {
	if d != nil {
		d.draining.Store(true)
	}
}

// Draining reports whether MarkDraining has been called.
func (d *DrainState) Draining() bool {
	return d != nil && d.draining.Load()
}

// ServerOption returns a Kitex server.Option that attaches TTHeader crrst=1 on
// responses once DrainState is draining.
func (d *DrainState) ServerOption() server.Option {
	return server.WithMetaHandler(&serverDrainMetaHandler{state: d})
}

type serverDrainMetaHandler struct {
	state *DrainState
}

func setConnResetTag(ri rpcinfo.RPCInfo) {
	if ri == nil || ri.To() == nil {
		return
	}
	if ei := rpcinfo.AsTaggable(ri.To()); ei != nil {
		ei.SetTag(rpcinfo.ConnResetTag, "1")
	}
	if mei := rpcinfo.AsMutableEndpointInfo(ri.To()); mei != nil {
		mei.SetTag(rpcinfo.ConnResetTag, "1")
	}
}

func (h *serverDrainMetaHandler) WriteMeta(ctx context.Context, msg remote.Message) (context.Context, error) {
	if h.state != nil && h.state.Draining() && msg != nil {
		setConnResetTag(msg.RPCInfo())
		if ti := msg.TransInfo(); ti != nil {
			if strInfo := ti.TransStrInfo(); strInfo != nil {
				strInfo[rtransmeta.HeaderConnectionReadyToReset] = "1"
			} else {
				ti.PutTransStrInfo(map[string]string{rtransmeta.HeaderConnectionReadyToReset: "1"})
			}
		}
	}
	return ctx, nil
}

func (h *serverDrainMetaHandler) ReadMeta(ctx context.Context, _ remote.Message) (context.Context, error) {
	return ctx, nil
}

type ipLookupFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

type dualSlotState struct {
	rawTarget string
	name      string
	specs     []string
	lookupIP  ipLookupFunc
	pool      *rconnpool.LongPool

	mu               sync.RWMutex
	drainingUntil    map[string]time.Time
	unreachableUntil map[string]time.Time
	cachedEndpoints  []string
	rrCounter        atomic.Uint64
}

func newDualSlotState(addr string, lookup ipLookupFunc, pool *rconnpool.LongPool) *dualSlotState {
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	specs := expandCandidateHostPorts(addr)
	id := resolverSeq.Add(1)
	return &dualSlotState{
		rawTarget:        addr,
		name:             fmt.Sprintf("teamie_dualslot_%d", id),
		specs:            specs,
		lookupIP:         lookup,
		pool:             pool,
		drainingUntil:    make(map[string]time.Time),
		unreachableUntil: make(map[string]time.Time),
		cachedEndpoints:  nil,
	}
}

func shouldAppendCanaryHost(host string) bool {
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if strings.Contains(host, ".") {
		return false
	}
	if strings.HasSuffix(host, "-canary") {
		return false
	}
	return true
}

func expandCandidateHostPorts(addr string) []string {
	parts := strings.Split(addr, ",")
	seen := make(map[string]struct{}, len(parts)*2)
	out := make([]string, 0, len(parts)*2)
	add := func(hp string) {
		hp = strings.TrimSpace(hp)
		if hp == "" {
			return
		}
		if _, exists := seen[hp]; exists {
			return
		}
		seen[hp] = struct{}{}
		out = append(out, hp)
	}
	for _, raw := range parts {
		hp := strings.TrimSpace(raw)
		if hp == "" {
			continue
		}
		add(hp)
		host, port, err := net.SplitHostPort(hp)
		if err == nil && shouldAppendCanaryHost(host) {
			add(net.JoinHostPort(host+"-canary", port))
		}
	}
	if len(out) == 0 && strings.TrimSpace(addr) != "" {
		out = append(out, strings.TrimSpace(addr))
	}
	return out
}

func (s *dualSlotState) markDraining(address string, ttl time.Duration) {
	if address == "" || ttl <= 0 {
		return
	}
	now := time.Now()
	until := now.Add(ttl)
	s.mu.Lock()
	cur, existed := s.drainingUntil[address]
	newlyDraining := !existed || !now.Before(cur)
	if !existed || until.After(cur) {
		s.drainingUntil[address] = until
	}
	// Clear transient unreachable cooldowns on sibling slots so newly started canary slots are probed immediately.
	for peer := range s.unreachableUntil {
		if peer != address {
			delete(s.unreachableUntil, peer)
		}
	}
	s.mu.Unlock()
	if newlyDraining && s.pool != nil {
		s.pool.Clean("tcp", address)
	}
}

func (s *dualSlotState) markUnreachable(address string, ttl time.Duration) {
	if address == "" || ttl <= 0 {
		return
	}
	now := time.Now()
	until := now.Add(ttl)
	s.mu.Lock()
	cur, existed := s.unreachableUntil[address]
	newlyUnreachable := !existed || !now.Before(cur)
	if !existed || until.After(cur) {
		s.unreachableUntil[address] = until
	}
	s.mu.Unlock()
	if newlyUnreachable && s.pool != nil {
		s.pool.Clean("tcp", address)
	}
}

func (s *dualSlotState) clearUnreachable(address string) {
	if address == "" {
		return
	}
	s.mu.Lock()
	delete(s.unreachableUntil, address)
	s.mu.Unlock()
}

func (s *dualSlotState) endpointTier(address string, now time.Time) int {
	s.mu.RLock()
	drainExp, draining := s.drainingUntil[address]
	unreachExp, unreach := s.unreachableUntil[address]
	s.mu.RUnlock()
	if draining && now.Before(drainExp) {
		return 2
	}
	if unreach && now.Before(unreachExp) {
		return 1
	}
	return 0
}

func (s *dualSlotState) isAvoided(address string, now time.Time) bool {
	return s.endpointTier(address, now) > 0
}

func (s *dualSlotState) hasBetterCachedPeer(address string) bool {
	now := time.Now()
	myTier := s.endpointTier(address, now)
	if myTier == 0 {
		return false
	}
	s.mu.RLock()
	cached := append([]string(nil), s.cachedEndpoints...)
	s.mu.RUnlock()
	for _, ep := range cached {
		if ep != address && s.endpointTier(ep, now) < myTier {
			return true
		}
	}
	return false
}

func (s *dualSlotState) resolveEndpoints(ctx context.Context) []string {
	seen := make(map[string]struct{}, len(s.specs)*2)
	out := make([]string, 0, len(s.specs)*2)
	add := func(ep string) {
		if ep == "" {
			return
		}
		if _, ok := seen[ep]; ok {
			return
		}
		seen[ep] = struct{}{}
		out = append(out, ep)
	}

	for _, spec := range s.specs {
		host, port, err := net.SplitHostPort(spec)
		if err != nil {
			add(spec)
			continue
		}
		if net.ParseIP(host) != nil {
			add(net.JoinHostPort(host, port))
			continue
		}
		lookupCtx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
		ips, lookupErr := s.lookupIP(lookupCtx, host)
		cancel()
		if lookupErr != nil || len(ips) == 0 {
			continue
		}
		for _, ip := range ips {
			if v4 := ip.IP.To4(); v4 != nil {
				add(net.JoinHostPort(v4.String(), port))
			}
		}
		for _, ip := range ips {
			if ip.IP.To4() == nil && len(ip.IP) > 0 {
				add(net.JoinHostPort(ip.IP.String(), port))
			}
		}
	}

	if len(out) == 0 && len(s.specs) > 0 {
		add(s.specs[0])
	}
	return out
}

func (s *dualSlotState) resolver() discovery.Resolver {
	return &discovery.SynthesizedResolver{
		TargetFunc: func(_ context.Context, _ rpcinfo.EndpointInfo) string {
			return s.rawTarget
		},
		ResolveFunc: func(ctx context.Context, _ string) (discovery.Result, error) {
			endpoints := s.resolveEndpoints(ctx)
			if len(endpoints) == 0 {
				return discovery.Result{}, errors.New("no rpc endpoint candidates")
			}
			s.mu.Lock()
			s.cachedEndpoints = append(s.cachedEndpoints[:0], endpoints...)
			s.mu.Unlock()

			insts := make([]discovery.Instance, 0, len(endpoints))
			for _, ep := range endpoints {
				insts = append(insts, discovery.NewInstance("tcp", ep, discovery.DefaultWeight, nil))
			}
			return discovery.Result{
				Cacheable: true,
				CacheKey:  s.rawTarget,
				Instances: insts,
			}, nil
		},
		DiffFunc: discovery.DefaultDiff,
		NameFunc: func() string {
			return s.name
		},
	}
}

// dualSlotBalancer selects healthy (non-avoided) instances in round-robin order without
// waiting for periodic lbcache ticks, and prevents retrying the same failed instance
// within a single RPC call's pre-send connection attempts.
type dualSlotBalancer struct {
	state   *dualSlotState
	pickers sync.Map // cacheKey -> []discovery.Instance
}

func (b *dualSlotBalancer) GetPicker(res discovery.Result) loadbalance.Picker {
	insts := res.Instances
	if res.Cacheable && res.CacheKey != "" {
		if cached, ok := b.pickers.Load(res.CacheKey); ok {
			insts = cached.([]discovery.Instance)
		} else {
			copied := append([]discovery.Instance(nil), res.Instances...)
			b.pickers.Store(res.CacheKey, copied)
			insts = copied
		}
	}
	return &dualSlotPicker{
		state: b.state,
		insts: insts,
		start: b.state.rrCounter.Add(1),
	}
}

func (b *dualSlotBalancer) Rebalance(ch discovery.Change) {
	if ch.Result.Cacheable && ch.Result.CacheKey != "" {
		copied := append([]discovery.Instance(nil), ch.Result.Instances...)
		b.pickers.Store(ch.Result.CacheKey, copied)
	}
}

func (b *dualSlotBalancer) Delete(ch discovery.Change) {
	if ch.Result.CacheKey != "" {
		b.pickers.Delete(ch.Result.CacheKey)
	}
}

func (b *dualSlotBalancer) Name() string {
	return "teamie_dualslot_rr"
}

type dualSlotPicker struct {
	state *dualSlotState
	insts []discovery.Instance
	start uint64
}

func (p *dualSlotPicker) Next(_ context.Context, _ interface{}) discovery.Instance {
	n := len(p.insts)
	if n == 0 {
		return nil
	}
	now := time.Now()
	for tier := 0; tier <= 2; tier++ {
		for i := 0; i < n; i++ {
			idx := int((p.start + uint64(i)) % uint64(n))
			cand := p.insts[idx]
			if cand.Address() != nil && p.state.endpointTier(cand.Address().String(), now) == tier {
				return cand
			}
		}
	}
	return p.insts[int(p.start%uint64(n))]
}

type clientDrainMetaHandler struct {
	state          *dualSlotState
	crossSlotAddrs *sync.Map // requestedAddress -> *atomic.Int32
}

func (h *clientDrainMetaHandler) WriteMeta(ctx context.Context, _ remote.Message) (context.Context, error) {
	return ctx, nil
}

func (h *clientDrainMetaHandler) ReadMeta(ctx context.Context, msg remote.Message) (context.Context, error) {
	if h.state == nil || msg == nil {
		return ctx, nil
	}
	ri := msg.RPCInfo()
	var remoteAddr string
	if ri != nil && ri.To() != nil && ri.To().Address() != nil {
		remoteAddr = ri.To().Address().String()
	}

	// Never pool a one-shot cross-slot fallback connection under the stale peer bucket.
	if remoteAddr != "" && h.crossSlotAddrs != nil {
		if v, ok := h.crossSlotAddrs.Load(remoteAddr); ok {
			cnt := v.(*atomic.Int32)
			for {
				cur := cnt.Load()
				if cur <= 0 {
					break
				}
				if cnt.CompareAndSwap(cur, cur-1) {
					setConnResetTag(ri)
					break
				}
			}
		}
	}

	crrst := false
	if ti := msg.TransInfo(); ti != nil {
		if val, ok := ti.TransStrInfo()[rtransmeta.HeaderConnectionReadyToReset]; ok && val != "" {
			crrst = true
		}
	}
	if !crrst && ri != nil && ri.To() != nil {
		if val, ok := ri.To().Tag(rpcinfo.ConnResetTag); ok && val != "" {
			crrst = true
		}
	}
	if crrst {
		setConnResetTag(ri)
		if remoteAddr != "" {
			h.state.markDraining(remoteAddr, defaultDrainingTTL)
		}
	}
	return ctx, nil
}

// failoverDialer performs dial-time endpoint failover strictly at TCP connect()
// before any RPC payload bytes are transmitted, preserving write idempotency.
type failoverDialer struct {
	inner          remote.Dialer
	state          *dualSlotState
	crossSlotAddrs *sync.Map
}

func (d *failoverDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		timeout = time.Second
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return d.inner.DialTimeout(network, address, timeout)
	}

	// Fast path: try the selected instance directly unless a strictly healthier cached peer exists.
	if !d.state.hasBetterCachedPeer(address) {
		attemptTimeout := timeout
		if len(d.state.specs) > 1 && attemptTimeout > 300*time.Millisecond {
			attemptTimeout = timeout / 2
		}
		conn, err := d.inner.DialTimeout(network, address, attemptTimeout)
		if err == nil {
			d.state.clearUnreachable(address)
			return conn, nil
		}
		d.state.markUnreachable(address, defaultUnreachableTTL)
		// If Balancer holds a healthier peer in its cached list, return ErrGetConnection so
		// Kitex's pre-send resolve middleware immediately picks that peer and pools under its own key.
		if d.state.hasBetterCachedPeer(address) {
			return nil, err
		}
		timeout -= attemptTimeout
		if timeout < 150*time.Millisecond {
			timeout = 150 * time.Millisecond
		}
	} else {
		return nil, fmt.Errorf("rpc slot %s is draining or unreachable", address)
	}

	// Fresh DNS fallback path: no healthier peer was in the cached list (e.g. <svc>-canary
	// appeared in Docker DNS after the last refresh tick). Re-resolve now and dial alternate candidates.
	fresh := d.state.resolveEndpoints(context.Background())
	deadline := time.Now().Add(timeout)
	var lastErr error
	for i, candidate := range fresh {
		if candidate == address {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		perAttempt := remaining
		if remCount := len(fresh) - i; remCount > 1 {
			perAttempt = remaining / time.Duration(remCount)
			if perAttempt < 150*time.Millisecond && remaining >= 150*time.Millisecond {
				perAttempt = 150 * time.Millisecond
			}
		}
		conn, err := d.inner.DialTimeout(network, candidate, perAttempt)
		if err == nil {
			d.state.clearUnreachable(candidate)
			if d.crossSlotAddrs != nil && address != "" {
				actual, _ := d.crossSlotAddrs.LoadOrStore(address, &atomic.Int32{})
				actual.(*atomic.Int32).Add(1)
			}
			return conn, nil
		}
		lastErr = err
		d.state.markUnreachable(candidate, defaultUnreachableTTL)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all rpc slot candidates unreachable for %s", address)
	}
	return nil, lastErr
}

func buildClientOptions(addr string, rpcTimeout, connectTimeout time.Duration, lookup ipLookupFunc) []client.Option {
	if rpcTimeout <= 0 {
		rpcTimeout = 3 * time.Second
	}
	if connectTimeout <= 0 {
		connectTimeout = time.Second
	}
	idleCfg := connpool.IdleConfig{
		MinIdlePerAddress: 0,
		MaxIdlePerAddress: 4,
		MaxIdleGlobal:     16,
		MaxIdleTimeout:    time.Second,
	}
	checkedCfg := connpool.CheckPoolConfig(idleCfg)
	longPool := rconnpool.NewLongPool("teamie-rpc", *checkedCfg)

	var crossSlotAddrs sync.Map
	state := newDualSlotState(addr, lookup, longPool)
	dialer := &failoverDialer{
		inner:          gonet.NewDialer(),
		state:          state,
		crossSlotAddrs: &crossSlotAddrs,
	}
	balancer := &dualSlotBalancer{state: state}

	return []client.Option{
		observability.RPCClient(),
		client.WithTransHandlerFactory(gonet.NewCliTransHandlerFactory()),
		client.WithMetaHandler(transmeta.ClientTTHeaderHandler),
		client.WithMetaHandler(&clientDrainMetaHandler{
			state:          state,
			crossSlotAddrs: &crossSlotAddrs,
		}),
		client.WithConnPool(longPool),
		client.WithResolver(state.resolver()),
		client.WithLoadBalancer(
			balancer,
			&lbcache.Options{
				RefreshInterval: defaultRefreshInterval,
				ExpireInterval:  defaultExpireInterval,
				Cacheable:       false,
			},
		),
		client.WithDialer(dialer),
		client.WithRPCTimeout(rpcTimeout),
		client.WithConnectTimeout(connectTimeout),
	}
}

// ClientOptions constructs the standard Kitex client options with ECS observability,
// bounded RPC/connection timeouts, dual-slot (<service> + <service>-canary) resolution,
// TTHeader crrst drain tracking, and pre-send dial-time failover.
func ClientOptions(addr string, rpcTimeout, connectTimeout time.Duration) []client.Option {
	return buildClientOptions(addr, rpcTimeout, connectTimeout, nil)
}

// ServerOptions constructs the standard Kitex server options for the given TCP address
// with ECS observability, TTHeader crrst metadata support, decoupled OS signal handling
// (so httpx orchestrates TEAMIE_DRAIN_GRACE before Stop), and optional payload size limit.
func ServerOptions(addr net.Addr, maxPayloadBytes int, extra ...server.Option) []server.Option {
	opts := make([]server.Option, 0, len(extra)+7)
	opts = append(opts,
		server.WithServiceAddr(addr),
		server.WithTransServerFactory(gonet.NewTransServerFactory()),
		server.WithTransHandlerFactory(gonet.NewSvrTransHandlerFactory()),
		server.WithExitSignal(func() <-chan error { return make(chan error) }),
		server.WithExitWaitTime(3*time.Second),
	)
	opts = append(opts, extra...)
	opts = append(opts,
		server.WithMetaHandler(transmeta.ServerTTHeaderHandler),
		observability.RPCServer(),
	)
	if maxPayloadBytes > 0 {
		opts = append(opts, server.WithCodec(codec.NewDefaultCodecWithSizeLimit(maxPayloadBytes)))
	}
	return opts
}
