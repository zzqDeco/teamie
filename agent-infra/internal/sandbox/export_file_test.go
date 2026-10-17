package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/remote/codec"
	"github.com/cloudwego/kitex/server"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
	"teamie/backend/pkg/imlimits"
)

func TestBinaryExportGeneratedKitexRoundTripAndFreshReads(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	workspace, err := s.workspace("agent-a")
	if err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte{0, 255, 128, 254}, imlimits.AttachmentBytes/4)
	if err = os.WriteFile(filepath.Join(workspace, "result.bin"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := sandboxservice.NewServer(s, server.WithListener(listener), server.WithCodec(codec.NewDefaultCodecWithSizeLimit(imlimits.AttachmentRequestBytes)), server.WithExitWaitTime(time.Second))
	done := make(chan error, 1)
	go func() { done <- rpc.Run() }()
	t.Cleanup(func() { _ = rpc.Stop(); <-done })
	remote, err := sandboxservice.NewClient("export-file-fixture", client.WithHostPorts(listener.Addr().String()), client.WithRPCTimeout(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	out, err := remote.ExportFile(context.Background(), &wire.SandboxExportRequest{ServiceToken: token, AgentId: "agent-a", Path: "/workspace/result.bin"})
	hash := sha256.Sum256(raw)
	if err != nil || out == nil || !out.Ok || !bytes.Equal(out.Content, raw) || out.GetSha256() != hex.EncodeToString(hash[:]) {
		t.Fatal("maximum binary export changed bytes", out, err)
	}
	// No externally committed upload exists yet: exporting is a fresh bounded
	// read and creates no immutable file snapshot or execution receipt.
	changed := []byte{0, 1, 255}
	if err = os.WriteFile(filepath.Join(workspace, "result.bin"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	out, err = remote.ExportFile(context.Background(), &wire.SandboxExportRequest{ServiceToken: token, AgentId: "agent-a", Path: "result.bin"})
	if err != nil || !out.Ok || !bytes.Equal(out.Content, changed) {
		t.Fatal("export invented a persistent snapshot", out, err)
	}
}

func TestBinaryExportConfinementAuthorizationAndFileLimits(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	workspace, err := s.workspace("agent-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.workspace("agent-b")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(other, "other.bin"), []byte("another Agent's result"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(workspace, "results"), 0700); err != nil {
		t.Fatal(err)
	}
	for filename, raw := range map[string][]byte{"results/good.bin": {0, 255, 128}, "empty": {}, "large": make([]byte, imlimits.AttachmentBytes+1)} {
		if err = os.WriteFile(filepath.Join(workspace, filename), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Symlink(filepath.Join(other, "other.bin"), filepath.Join(workspace, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("results/good.bin", filepath.Join(workspace, "inside-link")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("results", filepath.Join(workspace, "parent-link")); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mkfifo(filepath.Join(workspace, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"/etc/passwd", "../other.bin", "/workspace/../other.bin", "outside-link", "inside-link", "parent-link/good.bin", "results", "empty", "large", "pipe", "other.bin"} {
		t.Run(name, func(t *testing.T) {
			out, err := s.ExportFile(context.Background(), &wire.SandboxExportRequest{ServiceToken: token, AgentId: "agent-a", Path: name})
			if err != nil || out.Ok || len(out.Content) != 0 {
				t.Fatal("invalid export returned content", out, err)
			}
		})
	}
	for _, req := range []*wire.SandboxExportRequest{nil, {ServiceToken: "wrong", AgentId: "agent-a", Path: "results/good.bin"}, {ServiceToken: token, AgentId: "../agent-a", Path: "results/good.bin"}} {
		out, err := s.ExportFile(context.Background(), req)
		if err != nil || out.Ok || len(out.Content) != 0 {
			t.Fatal("unauthorized export", out, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := s.ExportFile(ctx, &wire.SandboxExportRequest{ServiceToken: token, AgentId: "agent-a", Path: "results/good.bin"})
	if err != nil || out.Ok || out.GetErrorCode() != "execution_canceled" {
		t.Fatal("canceled export", out, err)
	}
}
