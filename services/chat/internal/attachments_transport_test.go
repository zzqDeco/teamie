//go:build !windows

package chat

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	hertznetpoll "github.com/cloudwego/hertz/pkg/network/netpoll"

	"teamie/pkg/httpx"
	"teamie/pkg/imlimits"
)

// A recorder cannot detect buffers retained by the Hertz transport after a
// handler returns. Use the production adaptor and netpoll transport with real
// backpressure: the peer's receive window cannot accept a complete attachment.
func TestAttachmentDownloadHoldsCapacityUntilHertzFlushCompletes(t *testing.T) {
	s, _, conversation, owner, _ := setup(t)
	raw := bytes.Repeat([]byte{0, 255, 128, 254}, imlimits.AttachmentBytes/4)
	file, err := s.UploadAttachment(context.Background(), owner.ID, conversation.ID, "transport.bin", raw)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/conversations/" + conversation.ID + "/attachments/" + file.ID + "/content"
	handler := (&Handler{Store: s, Identity: attachmentIdentity{httpx.Session{User: owner}}}).Routes()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan string, 2)
	h := server.New(server.WithListener(listener), server.WithWriteTimeout(10*time.Second), server.WithReadTimeout(5*time.Second), server.WithDisablePrintRoute(true), server.WithOnAccept(func(conn net.Conn) context.Context {
		// Bound the server's kernel buffer too, so this remains deterministic on
		// hosts whose default send buffer can hold a complete 4 MiB file.
		wrapped, ok := conn.(*hertznetpoll.Conn)
		if !ok {
			t.Errorf("expected production netpoll connection, got %T", conn)
			return context.Background()
		}
		fd, ok := wrapped.Conn.(interface{ Fd() int })
		if !ok {
			t.Errorf("netpoll connection has no fd: %T", wrapped.Conn)
		} else if e := syscall.SetsockoptInt(fd.Fd(), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 32*1024); e != nil {
			t.Error(e)
		}
		return context.Background()
	}))
	h.Any("/*path", adaptor.HertzHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if id := r.URL.Query().Get("slow"); id != "" {
			completed <- id
		}
	})))
	runDone := make(chan error, 1)
	go func() { runDone <- h.Run() }()
	t.Cleanup(func() {
		_ = h.Close()
		select {
		case err := <-runDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Hertz did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for !h.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("Hertz did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if h.GetTransporterName() != "netpoll" {
		t.Fatal("test must use production netpoll transport", h.GetTransporterName())
	}
	address := listener.Addr().String()
	slow := func(id string) *net.TCPConn {
		t.Helper()
		dialer := net.Dialer{Timeout: time.Second, Control: func(_, _ string, c syscall.RawConn) error {
			var optionErr error
			if err := c.Control(func(fd uintptr) {
				optionErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096)
			}); err != nil {
				return err
			}
			return optionErr
		}}
		conn, err := dialer.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		tcp := conn.(*net.TCPConn)
		t.Cleanup(func() { _ = tcp.SetLinger(0); _ = tcp.Close() })
		_ = tcp.SetDeadline(time.Now().Add(8 * time.Second))
		if _, err = fmt.Fprintf(tcp, "GET %s?slow=%s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, id, address); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReaderSize(tcp, 1024), nil)
		if err != nil || response.StatusCode != http.StatusOK || response.ContentLength != int64(len(raw)) {
			t.Fatal("slow connection did not begin binary response", response, err)
		}
		// Leave the body unread until this connection is reset below.
		return tcp
	}
	first, second := slow("first"), slow("second")
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Get("http://" + address + path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatal("two stalled downloads must reject a third transfer", response.StatusCode)
	}
	select {
	case id := <-completed:
		t.Fatal("download released capacity before its bytes flushed", id)
	default:
	}
	waitReset := func(conn *net.TCPConn, id string, remaining int) {
		t.Helper()
		_ = conn.SetLinger(0)
		_ = conn.Close()
		select {
		case got := <-completed:
			if got != id || len(attachmentTransfers) != remaining {
				t.Fatal("disconnect did not release exactly its transfer", got, len(attachmentTransfers))
			}
		case <-time.After(3 * time.Second):
			t.Fatal("disconnected download retained capacity")
		}
	}
	waitReset(first, "first", 1)
	// The reclaimed slot must serve the complete original binary while the
	// second connection still holds the other slot.
	response, err = client.Get("http://" + address + path)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(actual, raw) || response.Header.Get("X-Attachment-SHA256") != file.SHA256 {
		t.Fatal("download after disconnect changed binary bytes", response.StatusCode, len(actual), err)
	}
	waitReset(second, "second", 0)
}
