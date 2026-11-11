package rpcx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/gopkg/protocol/thrift"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"
)

type testEchoService interface {
	Mutate(ctx context.Context, reqID string) (string, error)
}

type testMutateArgs struct {
	ReqID string
}

func (p *testMutateArgs) BLength() int {
	l := 0
	if p != nil {
		l += thrift.Binary.FieldBeginLength() + thrift.Binary.StringLength(p.ReqID)
	}
	return l + thrift.Binary.FieldStopLength()
}

func (p *testMutateArgs) FastWrite(buf []byte) int {
	return p.FastWriteNocopy(buf, nil)
}

func (p *testMutateArgs) FastWriteNocopy(buf []byte, w thrift.NocopyWriter) int {
	offset := 0
	if p != nil {
		offset += thrift.Binary.WriteFieldBegin(buf[offset:], thrift.STRING, 1)
		offset += thrift.Binary.WriteStringNocopy(buf[offset:], w, p.ReqID)
	}
	offset += thrift.Binary.WriteFieldStop(buf[offset:])
	return offset
}

func (p *testMutateArgs) FastRead(buf []byte) (int, error) {
	var offset int
	for {
		ft, fid, l, err := thrift.Binary.ReadFieldBegin(buf[offset:])
		offset += l
		if err != nil {
			return offset, err
		}
		if ft == thrift.STOP {
			break
		}
		if fid == 1 && ft == thrift.STRING {
			s, sl, rerr := thrift.Binary.ReadString(buf[offset:])
			offset += sl
			if rerr != nil {
				return offset, rerr
			}
			p.ReqID = s
			continue
		}
		sl, serr := thrift.Binary.Skip(buf[offset:], ft)
		offset += sl
		if serr != nil {
			return offset, serr
		}
	}
	return offset, nil
}

type testMutateResult struct {
	Slot string
}

func (p *testMutateResult) BLength() int {
	l := 0
	if p != nil {
		l += thrift.Binary.FieldBeginLength() + thrift.Binary.StringLength(p.Slot)
	}
	return l + thrift.Binary.FieldStopLength()
}

func (p *testMutateResult) FastWrite(buf []byte) int {
	return p.FastWriteNocopy(buf, nil)
}

func (p *testMutateResult) FastWriteNocopy(buf []byte, w thrift.NocopyWriter) int {
	offset := 0
	if p != nil {
		offset += thrift.Binary.WriteFieldBegin(buf[offset:], thrift.STRING, 0)
		offset += thrift.Binary.WriteStringNocopy(buf[offset:], w, p.Slot)
	}
	offset += thrift.Binary.WriteFieldStop(buf[offset:])
	return offset
}

func (p *testMutateResult) FastRead(buf []byte) (int, error) {
	var offset int
	for {
		ft, fid, l, err := thrift.Binary.ReadFieldBegin(buf[offset:])
		offset += l
		if err != nil {
			return offset, err
		}
		if ft == thrift.STOP {
			break
		}
		if fid == 0 && ft == thrift.STRING {
			s, sl, rerr := thrift.Binary.ReadString(buf[offset:])
			offset += sl
			if rerr != nil {
				return offset, rerr
			}
			p.Slot = s
			continue
		}
		sl, serr := thrift.Binary.Skip(buf[offset:], ft)
		offset += sl
		if serr != nil {
			return offset, serr
		}
	}
	return offset, nil
}

func newTestServiceInfo() *serviceinfo.ServiceInfo {
	methods := map[string]serviceinfo.MethodInfo{
		"Mutate": serviceinfo.NewMethodInfo(
			func(ctx context.Context, handler interface{}, arg, result interface{}) error {
				a := arg.(*testMutateArgs)
				r := result.(*testMutateResult)
				slot, err := handler.(testEchoService).Mutate(ctx, a.ReqID)
				if err != nil {
					return err
				}
				r.Slot = slot
				return nil
			},
			func() interface{} { return &testMutateArgs{} },
			func() interface{} { return &testMutateResult{} },
			false,
			serviceinfo.WithStreamingMode(serviceinfo.StreamingNone),
		),
	}
	return &serviceinfo.ServiceInfo{
		ServiceName:  "DualSlotTestService",
		HandlerType:  (*testEchoService)(nil),
		Methods:      methods,
		PayloadCodec: serviceinfo.Thrift,
		Extra:        map[string]interface{}{"PackageName": "rpcx"},
	}
}

type slotHandler struct {
	slot string
	seen *sync.Map
	hits atomic.Int64
}

func (h *slotHandler) Mutate(_ context.Context, reqID string) (string, error) {
	h.hits.Add(1)
	if h.seen != nil {
		if prev, loaded := h.seen.LoadOrStore(reqID, h.slot); loaded {
			return "", fmt.Errorf("non-idempotent duplicate execution of %s on %s (already executed on %v)", reqID, h.slot, prev)
		}
	}
	time.Sleep(time.Millisecond)
	return h.slot, nil
}

func reservePortAddr(t *testing.T) *net.TCPAddr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()
	return tcpAddr
}

func startKitexSlot(t *testing.T, addr *net.TCPAddr, handler testEchoService, drain *DrainState) (server.Server, chan error) {
	t.Helper()
	svcInfo := newTestServiceInfo()
	sOpts := ServerOptions(addr, 1024*1024, drain.ServerOption())
	srv := server.NewServer(sOpts...)
	if err := srv.RegisterService(svcInfo, handler); err != nil {
		t.Fatalf("RegisterService: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr.String(), 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return srv, errCh
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for Kitex server on %s", addr.String())
	return nil, nil
}

func TestClientAndServerOptionsDefaults(t *testing.T) {
	cOpts := ClientOptions("127.0.0.1:8881", 0, 0)
	if len(cOpts) < 4 {
		t.Fatalf("expected at least 4 client options, got %d", len(cOpts))
	}
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sOpts := ServerOptions(addr, 1024*1024)
	if len(sOpts) < 3 {
		t.Fatalf("expected at least 3 server options, got %d", len(sOpts))
	}
	_ = ClientOptions("127.0.0.1:8881", 5*time.Second, 2*time.Second)
}

func TestExpandCandidateHostPorts(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{
			in:   "identity:8881",
			want: []string{"identity:8881", "identity-canary:8881"},
		},
		{
			in:   "identity-canary:8881",
			want: []string{"identity-canary:8881"},
		},
		{
			in:   "127.0.0.1:8881",
			want: []string{"127.0.0.1:8881"},
		},
		{
			in:   "127.0.0.1:50001, 127.0.0.1:50002",
			want: []string{"127.0.0.1:50001", "127.0.0.1:50002"},
		},
	}
	for _, tc := range cases {
		if got := expandCandidateHostPorts(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("expandCandidateHostPorts(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDualSlotLosslessCutoverCommaSeparatedEndpoints(t *testing.T) {
	addrA := reservePortAddr(t)
	addrB := reservePortAddr(t)

	var seen sync.Map
	drainA := NewDrainState()
	handlerA := &slotHandler{slot: "slot-a", seen: &seen}
	srvA, errChA := startKitexSlot(t, addrA, handlerA, drainA)

	svcInfo := newTestServiceInfo()
	cli, err := client.NewClient(
		svcInfo,
		append(
			ClientOptions(addrA.String()+","+addrB.String(), 2*time.Second, 600*time.Millisecond),
			client.WithDestService("DualSlotTestService"),
		)...,
	)
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	stopLoad := make(chan struct{})
	var (
		wg        sync.WaitGroup
		callSeq   atomic.Uint64
		totalOK   atomic.Int64
		firstErr  atomic.Value
		errCount  atomic.Int64
	)

	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopLoad:
					return
				default:
				}
				id := fmt.Sprintf("req-%d", callSeq.Add(1))
				args := &testMutateArgs{ReqID: id}
				res := &testMutateResult{}
				if callErr := cli.Call(context.Background(), "Mutate", args, res); callErr != nil {
					if errCount.Add(1) == 1 {
						firstErr.Store(callErr.Error())
					}
				} else if res.Slot == "" {
					if errCount.Add(1) == 1 {
						firstErr.Store("empty slot in response")
					}
				} else {
					totalOK.Add(1)
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}

	// Phase 1: Slot A serves alone while Slot B is not listening yet.
	time.Sleep(120 * time.Millisecond)

	// Phase 2: Start Slot B (canary).
	drainB := NewDrainState()
	handlerB := &slotHandler{slot: "slot-b", seen: &seen}
	srvB, errChB := startKitexSlot(t, addrB, handlerB, drainB)
	defer func() {
		if closer, ok := cli.(io.Closer); ok {
			_ = closer.Close()
		}
		_ = srvB.Stop()
		<-errChB
	}()
	time.Sleep(100 * time.Millisecond)

	// Phase 3: Slot A enters graceful drain (crrst=1) for 150ms, then stops.
	drainA.MarkDraining()
	time.Sleep(150 * time.Millisecond)
	if err := srvA.Stop(); err != nil {
		t.Fatalf("srvA.Stop: %v", err)
	}
	<-errChA

	// Phase 4: Continue concurrent RPCs with only Slot B active.
	time.Sleep(200 * time.Millisecond)
	close(stopLoad)
	wg.Wait()

	if errs := errCount.Load(); errs != 0 {
		t.Fatalf("expected 0 RPC errors during A->B cutover, got %d (first: %v)", errs, firstErr.Load())
	}
	if totalOK.Load() < 100 {
		t.Fatalf("expected >=100 successful RPC calls, got %d", totalOK.Load())
	}
	if handlerA.hits.Load() == 0 || handlerB.hits.Load() == 0 {
		t.Fatalf("expected both slots to serve traffic, hits A=%d B=%d", handlerA.hits.Load(), handlerB.hits.Load())
	}
}

func TestDualSlotDockerServiceCanaryDNSResolution(t *testing.T) {
	// Simulate Docker bridge DNS where <service> and <service>-canary share the same RPC port
	// on distinct loopback IPs (127.0.0.1 vs ::1 if available, or dynamic DNS flip on 127.0.0.1).
	// Even on single-IP loopback, verify <service>-canary lookup activates seamlessly when <service> drops out of DNS.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	var (
		dnsMu      sync.RWMutex
		primaryUp  = true
		canaryUp   = false
	)
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		dnsMu.RLock()
		defer dnsMu.RUnlock()
		switch host {
		case "chat":
			if primaryUp {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			}
			return nil, errors.New("no such host")
		case "chat-canary":
			if canaryUp {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			}
			return nil, errors.New("no such host")
		default:
			return nil, errors.New("unexpected host: " + host)
		}
	}

	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	drain := NewDrainState()
	handler := &slotHandler{slot: "canary-dns"}
	srv, errCh := startKitexSlot(t, addr, handler, drain)
	defer func() {
		_ = srv.Stop()
		<-errCh
	}()

	svcInfo := newTestServiceInfo()
	opts := append(
		buildClientOptions("chat:"+strconv.Itoa(port), 2*time.Second, 500*time.Millisecond, lookup),
		client.WithDestService("DualSlotTestService"),
	)
	cli, err := client.NewClient(svcInfo, opts...)
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}
	if closer, ok := cli.(io.Closer); ok {
		defer closer.Close()
	}

	// Flip DNS so only chat-canary resolves while chat returns NXDOMAIN.
	dnsMu.Lock()
	primaryUp = false
	canaryUp = true
	dnsMu.Unlock()

	for i := 0; i < 10; i++ {
		res := &testMutateResult{}
		if err := cli.Call(context.Background(), "Mutate", &testMutateArgs{ReqID: fmt.Sprintf("dns-%d", i)}, res); err != nil {
			t.Fatalf("RPC call %d via chat-canary DNS failed: %v", i, err)
		}
		if res.Slot != "canary-dns" {
			t.Fatalf("unexpected slot %q", res.Slot)
		}
	}
}
