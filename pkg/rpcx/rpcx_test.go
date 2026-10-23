package rpcx

import (
	"net"
	"testing"
	"time"
)

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
