package rpc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/realtimeservice"
	"teamie/pkg/rpcx"
	"teamie/services/realtime/internal/app"
	"teamie/services/realtime/internal/domain"
)

type failingPresenceStore struct {
	mu  sync.Mutex
	err error
}

func (s *failingPresenceStore) SaveUserPresence(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}

func (s *failingPresenceStore) GetUsersPresence(_ context.Context, _ []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return map[string]string{}, nil
}

type stubFlags struct {
	mu       sync.Mutex
	presence bool
}

func (f *stubFlags) TypingPublishEnabled() bool { return true }
func (f *stubFlags) PresencePublishEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.presence
}
func (f *stubFlags) SetPresencePublish(v bool) {
	f.mu.Lock()
	f.presence = v
	f.mu.Unlock()
}
func (f *stubFlags) ReadReceiptsViewEnabled() bool { return true }

type stubClient struct {
	uid string
}

func (c *stubClient) UserID() string      { return c.uid }
func (c *stubClient) Token() string       { return "tok-" + c.uid }
func (c *stubClient) Send(_ domain.Event) {}
func (c *stubClient) Close()              {}

func TestRealtimeRPCGetPresenceAuthBoundsStatesAndFlags(t *testing.T) {
	ctx := context.Background()
	var clockMu sync.Mutex
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	tracker := &domain.PresenceTracker{
		IdleTimeout:  5 * time.Minute,
		OfflineGrace: 30 * time.Second,
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		},
	}
	flags := &stubFlags{presence: true}
	pStore := &failingPresenceStore{}
	streamApp := &app.StreamApp{
		Flags:         flags,
		Presence:      tracker,
		PresenceStore: pStore,
	}

	// Missing configured REALTIME_RPC_TOKEN fails closed.
	unconfigured := &Server{App: streamApp}
	if resp, err := unconfigured.GetPresence(ctx, &im.PresenceRequest{ServiceToken: "anything", UserIds: []string{"u1"}}); err != nil || resp.Ok || resp.ErrorCode != "forbidden" {
		t.Fatalf("expected unconfigured server to fail closed, got %+v err=%v", resp, err)
	}

	srv := &Server{
		App:          streamApp,
		ServiceToken: "realtime-secret-token",
	}

	// Wrong or empty token fails closed.
	for _, bad := range []string{"", "wrong-token"} {
		if resp, err := srv.GetPresence(ctx, &im.PresenceRequest{ServiceToken: bad, UserIds: []string{"u1"}}); err != nil || resp.Ok || resp.ErrorCode != "forbidden" {
			t.Fatalf("expected bad token %q to fail closed, got %+v err=%v", bad, resp, err)
		}
	}

	// Over-limit batch rejected.
	tooMany := make([]string, domain.MaxPresenceLookupUsers+1)
	for i := range tooMany {
		tooMany[i] = "u"
	}
	if resp, err := srv.GetPresence(ctx, &im.PresenceRequest{ServiceToken: "realtime-secret-token", UserIds: tooMany}); err != nil || resp.Ok || resp.ErrorCode != "invalid_request" {
		t.Fatalf("expected over-limit batch to fail with invalid_request, got %+v err=%v", resp, err)
	}

	// Connect human online, human away, human disconnected within grace, human offline, and an Agent connection.
	cOnline := &stubClient{uid: "u-online"}
	cAway := &stubClient{uid: "u-away"}
	cGrace := &stubClient{uid: "u-grace"}
	cAgent := &stubClient{uid: "agent-1"}

	streamApp.ConnectPresence(ctx, nil, cOnline, domain.User{ID: "u-online", Kind: "human"})
	streamApp.ConnectPresence(ctx, nil, cAway, domain.User{ID: "u-away", Kind: "human"})
	if err := streamApp.ReportPresence(ctx, nil, cAway, domain.User{ID: "u-away", Kind: "human"}, "idle"); err != nil {
		t.Fatal(err)
	}
	streamApp.ConnectPresence(ctx, nil, cGrace, domain.User{ID: "u-grace", Kind: "human"})
	streamApp.DisconnectPresence(nil, cGrace)
	streamApp.ConnectPresence(ctx, nil, cAgent, domain.User{ID: "agent-1", Kind: "agent"})

	// Exercise real Kitex server + client transport.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	kitexSrv := realtimeservice.NewServer(srv, rpcx.ServerOptions(tcpAddr, 0)...)
	t.Cleanup(func() { _ = kitexSrv.Stop() })
	go func() { _ = kitexSrv.Run() }()

	cli, err := realtimeservice.NewClient("teamie.realtime", rpcx.ClientOptions(addr, 2*time.Second, time.Second)...)
	if err != nil {
		t.Fatal(err)
	}

	var resp *im.PresenceResponse
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err = cli.GetPresence(ctx, &im.PresenceRequest{
			ServiceToken: "realtime-secret-token",
			UserIds:      []string{"u-online", "u-away", "u-grace", "u-offline", "agent-1", "u-online"},
		})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("kitex GetPresence failed: %v", err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !resp.Ok || len(resp.Presence) != 3 {
		t.Fatalf("unexpected presence response: %+v", resp)
	}
	got := map[string]string{}
	for _, p := range resp.Presence {
		got[p.UserId] = p.State
	}
	if got["u-online"] != "online" || got["u-away"] != "away" || got["u-grace"] != "online" || got["agent-1"] != "" || got["u-offline"] != "" {
		t.Fatalf("unexpected presence map: %+v", got)
	}

	// Advance past 30s grace period -> u-grace becomes offline and omitted.
	clockMu.Lock()
	now = now.Add(31 * time.Second)
	clockMu.Unlock()
	resp, err = cli.GetPresence(ctx, &im.PresenceRequest{
		ServiceToken: "realtime-secret-token",
		UserIds:      []string{"u-online", "u-away", "u-grace"},
	})
	if err != nil || !resp.Ok || len(resp.Presence) != 2 {
		t.Fatalf("expected u-grace offline after grace window: %+v err=%v", resp, err)
	}

	// Disabling im.presence.publish returns empty presence snapshot.
	flags.SetPresencePublish(false)
	resp, err = cli.GetPresence(ctx, &im.PresenceRequest{
		ServiceToken: "realtime-secret-token",
		UserIds:      []string{"u-online", "u-away"},
	})
	if err != nil || !resp.Ok || len(resp.Presence) != 0 {
		t.Fatalf("expected empty presence when im.presence.publish=false: %+v err=%v", resp, err)
	}

	// Redis presence-store failure must propagate as Ok=false, ErrorCode=presence_unavailable
	// rather than returning a partial local snapshot.
	flags.SetPresencePublish(true)
	pStore.mu.Lock()
	pStore.err = errors.New("redis down")
	pStore.mu.Unlock()
	resp, err = cli.GetPresence(ctx, &im.PresenceRequest{
		ServiceToken: "realtime-secret-token",
		UserIds:      []string{"u-online", "u-away"},
	})
	if err != nil || resp.Ok || resp.ErrorCode != "presence_unavailable" {
		t.Fatalf("expected presence_unavailable on store failure, got %+v err=%v", resp, err)
	}
}
