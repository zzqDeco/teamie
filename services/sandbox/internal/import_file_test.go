package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/remote/codec"
	"github.com/cloudwego/kitex/server"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/sandboxservice"
	"teamie/pkg/imlimits"
)

func importRequest(raw []byte) *wire.SandboxImportRequest {
	h := sha256.Sum256(raw)
	return &wire.SandboxImportRequest{ServiceToken: token, AgentId: "agent-a", RunId: "run", RequestId: "stable-import", Path: "/workspace/attachments/raw.bin", Content: raw, Sha256: hex.EncodeToString(h[:])}
}

func TestBinaryImportGeneratedKitexRoundTrip(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := sandboxservice.NewServer(s, server.WithListener(listener), server.WithCodec(codec.NewDefaultCodecWithSizeLimit(imlimits.AttachmentRequestBytes)), server.WithExitWaitTime(time.Second))
	done := make(chan error, 1)
	go func() { done <- rpc.Run() }()
	t.Cleanup(func() { _ = rpc.Stop(); <-done })
	remote, err := sandboxservice.NewClient("attachment-sandbox-fixture", client.WithHostPorts(listener.Addr().String()), client.WithRPCTimeout(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte{0, 255, 128, 254}, imlimits.AttachmentBytes/4)
	r := importRequest(raw)
	out, err := remote.ImportFile(context.Background(), r)
	if err != nil || !out.Ok {
		t.Fatal("maximum binary Kitex transfer", out, err)
	}
	workspace, _ := s.workspace(r.AgentId)
	actual, err := os.ReadFile(filepath.Join(workspace, "attachments/raw.bin"))
	if err != nil || !bytes.Equal(actual, raw) {
		t.Fatal("binary RPC changed bytes", err)
	}
}

func TestBinaryImportByteEqualityReceiptsCrashAndIsolation(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	raw := bytes.Repeat([]byte{0, 255, 128, 254, 0, 1, 13, 10}, 40000)
	r := importRequest(raw)
	call := func() *wire.SandboxWorkspaceResponse {
		t.Helper()
		out, e := s.ImportFile(context.Background(), r)
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	if res := call(); !res.Ok {
		t.Fatal(res)
	}
	workspace, _ := s.workspace(r.AgentId)
	name := filepath.Join(workspace, "attachments/raw.bin")
	b, e := os.ReadFile(name)
	if e != nil || !bytes.Equal(b, raw) {
		t.Fatal("binary bytes changed", e)
	}
	if res := call(); !res.Ok {
		t.Fatal("same request must replay", res)
	}
	// A lost result after publication reconciles the exact intended bytes.
	receiptPath := filepath.Join(s.StateRoot, digest(r.AgentId+"\x00import\x00"+r.RequestId)+".file.json")
	b, e = os.ReadFile(receiptPath)
	if e != nil {
		t.Fatal(e)
	}
	var intent fileReceipt
	if json.Unmarshal(b, &intent) != nil {
		t.Fatal("receipt malformed")
	}
	intent.State = "intent"
	if e = writeAtomicJSON(receiptPath, intent); e != nil {
		t.Fatal(e)
	}
	if res := call(); !res.Ok {
		t.Fatal("binary intent could not reconcile", res)
	}
	// A receipt replay never replaces edits made after the original import.
	if e = os.WriteFile(name, []byte("later user edit"), 0666); e != nil {
		t.Fatal(e)
	}
	if res := call(); !res.Ok {
		t.Fatal(res)
	}
	b, _ = os.ReadFile(name)
	if string(b) != "later user edit" {
		t.Fatal("replay overwrote later edits")
	}
	intent.State = "intent"
	_ = writeAtomicJSON(receiptPath, intent)
	if res := call(); res.GetErrorCode() != "execution_unknown" {
		t.Fatal("ambiguous intent reran", res)
	}
	r.Path = "different.bin"
	if res := call(); res.GetErrorCode() != "request_conflict" {
		t.Fatal("identity conflict", res)
	}
	r.Path = "/workspace/attachments/raw.bin"
	r.AgentId = "agent-b"
	if res := call(); !res.Ok {
		t.Fatal("different Agent independent receipt", res)
	}
	other, _ := s.workspace(r.AgentId)
	b, _ = os.ReadFile(filepath.Join(other, "attachments/raw.bin"))
	if !bytes.Equal(b, raw) {
		t.Fatal("Agent workspace isolation")
	}
	if strings.Contains(intent.DataJSON, "base64") || strings.Contains(intent.DataJSON, "content") {
		t.Fatal("binary leaked into observation")
	}
}

func TestBinaryImportPathsLimitsOverwriteAndQuota(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	r := importRequest([]byte{0, 255})
	call := func() *wire.SandboxWorkspaceResponse { out, _ := s.ImportFile(context.Background(), r); return out }
	r.ServiceToken = "invalid"
	if res := call(); res.GetErrorCode() != "unauthorized" {
		t.Fatal(res)
	}
	r.ServiceToken = token
	for _, name := range []string{"../outside", "/tmp/host-file", "/workspace/../../host", "bad\\path", "."} {
		r.Path = name
		if res := call(); res.GetErrorCode() != "invalid_path" {
			t.Fatal(name, res)
		}
	}
	r.Path = "symlink/escape.bin"
	workspace, _ := s.workspace(r.AgentId)
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(workspace, "symlink")); e != nil {
		t.Fatal(e)
	}
	if res := call(); res.Ok {
		t.Fatal("followed parent symlink", res)
	}
	if _, e := os.Stat(filepath.Join(outside, "escape.bin")); !os.IsNotExist(e) {
		t.Fatal("wrote outside workspace")
	}
	r.Path = "safe.bin"
	if res := call(); !res.Ok {
		t.Fatal(res)
	}
	r.RequestId = "second-import"
	if res := call(); res.GetErrorCode() != "file_exists" {
		t.Fatal("overwrote existing file", res)
	}
	r.Path = "too-large"
	r.Content = make([]byte, imlimits.AttachmentBytes+1)
	if res := call(); res.GetErrorCode() != "file_too_large" {
		t.Fatal(res)
	}
	r.Content = []byte{0, 255}
	r.Sha256 = "wrong"
	if res := call(); res.GetErrorCode() != "content_mismatch" {
		t.Fatal(res)
	}
	r = importRequest([]byte{1, 2, 3})
	r.RequestId = "quota-import"
	r.Path = "quota.bin"
	f, e := os.Create(filepath.Join(workspace, "quota-existing"))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(MaxWorkspaceBytes); e != nil {
		t.Fatal(e)
	}
	_ = f.Close()
	if res := call(); res.GetErrorCode() != "workspace_capacity" {
		t.Fatal("workspace quota bypass", res)
	}
	root, e := os.OpenRoot(workspace)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if workspaceCapacity(root, "new", 1) {
		t.Fatal("quota accounting accepted overflow")
	}
}

func TestBinaryImportAtTransportMaximumAndCancellation(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	r := importRequest(bytes.Repeat([]byte{0, 254}, imlimits.AttachmentBytes/2))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, _ := s.ImportFile(ctx, r); out.GetErrorCode() != "execution_canceled" {
		t.Fatal(out)
	}
	if out, _ := s.ImportFile(context.Background(), r); !out.Ok {
		t.Fatal("maximum upload unsupported by sandbox", out)
	}
}
