package sandbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The Docker wire fixture verifies attachment precedes start, multiplex framing,
// overflow termination and cleanup without access to any real host daemon.
func TestDockerProtocolAttachBeforeStartOverflowAndCleanup(t *testing.T) {
	var mu sync.Mutex
	attached, started, killed, removed := false, false, false, false
	begin := make(chan struct{})
	exited := make(chan struct{})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/create"):
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": "container-fixed"})
		case strings.HasSuffix(r.URL.Path, "/attach"):
			mu.Lock()
			attached = true
			mu.Unlock()
			conn, buf, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			defer conn.Close()
			_, _ = fmt.Fprint(buf, "HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
			_ = buf.Flush()
			<-begin
			header := make([]byte, 8)
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], 100000)
			_, _ = buf.Write(header)
			_, _ = buf.WriteString(strings.Repeat("x", 100000))
			_ = buf.Flush()
			<-exited
		case strings.HasSuffix(r.URL.Path, "/start"):
			mu.Lock()
			if !attached {
				t.Error("container started before output attachment")
			}
			started = true
			mu.Unlock()
			close(begin)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/kill"):
			mu.Lock()
			killed = true
			mu.Unlock()
			close(exited)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/wait"):
			<-exited
			_ = json.NewEncoder(w).Encode(map[string]int{"StatusCode": 137})
		case r.Method == "DELETE":
			mu.Lock()
			removed = true
			mu.Unlock()
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 400)
		}
	}))
	defer fixture.Close()
	client := fixture.Client()
	tr := client.Transport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{}).DialContext
	client.Transport = roundTripTarget{base: fixture.URL, transport: tr}
	docker := &Docker{HTTP: client, prefix: "/v1.45"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, e := docker.Run(ctx, Job{Image: image, Command: "untrusted", Workspace: "/fixed"})
	if e != nil || !res.Truncated || res.ExitCode != 137 || len(res.Stdout) != OutputLimit {
		t.Fatal(res, e)
	}
	mu.Lock()
	defer mu.Unlock()
	if !attached || !started || !killed || !removed {
		t.Fatal(attached, started, killed, removed)
	}
}

type roundTripTarget struct {
	base      string
	transport http.RoundTripper
}

func (r roundTripTarget) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u, e := url.Parse(r.base)
	if e != nil {
		return nil, e
	}
	copy.URL.Scheme = u.Scheme
	copy.URL.Host = u.Host
	return r.transport.RoundTrip(copy)
}

func TestDockerWaitBodyCancellationClassificationAndCleanup(t *testing.T) {
	for _, test := range []struct {
		name                   string
		deadline, cleanupFails bool
		code                   string
	}{
		{name: "deadline after response headers", deadline: true, code: "execution_timeout"},
		{name: "explicit cancellation after response headers", code: "execution_canceled"},
		{name: "cleanup failure remains unknown", deadline: true, cleanupFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var removed atomic.Bool
			waitHeaders := make(chan struct{})
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/create"):
					_ = json.NewEncoder(w).Encode(map[string]string{"Id": "container-fixed"})
				case strings.HasSuffix(r.URL.Path, "/attach"):
					// Real Docker upgrades this stream before process start.
					conn, buf, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					defer conn.Close()
					_, _ = fmt.Fprint(buf, "HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
					_ = buf.Flush()
					// Await the client's stream close on cancellation.
					_, _ = io.Copy(io.Discard, conn)
				case strings.HasSuffix(r.URL.Path, "/start"):
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/wait"):
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					close(waitHeaders)
					// The body never completes, reproducing the hosted Docker timeout path.
					<-r.Context().Done()
				case r.Method == "DELETE":
					removed.Store(true)
					if test.cleanupFails {
						http.Error(w, "fixture cleanup failure", 500)
					} else {
						w.WriteHeader(204)
					}
				default:
					http.Error(w, "unexpected Docker request", 400)
				}
			}))
			defer fixture.Close()
			client := fixture.Client()
			tr := client.Transport.(*http.Transport).Clone()
			tr.Proxy = nil
			client.Transport = roundTripTarget{base: fixture.URL, transport: tr}
			docker := &Docker{HTTP: client, prefix: "/v1.45"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
				defer cancel()
			} else {
				go func() { <-waitHeaders; cancel() }()
			}
			result, e := docker.Run(ctx, Job{Image: image, Command: "sleep 10", Workspace: "/fixed"})
			select {
			case <-waitHeaders:
			default:
				t.Fatal("fixture did not reach flushed wait headers")
			}
			if !removed.Load() {
				t.Fatal("ephemeral container was not removed")
			}
			if test.cleanupFails {
				if e == nil {
					t.Fatal("cleanup failure was incorrectly treated as a confirmed cancellation")
				}
				return
			}
			if e != nil || result.ErrorCode != test.code || result.ExitCode != -1 {
				t.Fatalf("code=%q exit=%d error=%v", result.ErrorCode, result.ExitCode, e)
			}
		})
	}
}
