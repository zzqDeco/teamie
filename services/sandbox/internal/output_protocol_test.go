package sandbox

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDockerTimestampStreamAndReleaseUpgrade(t *testing.T) {
	released := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			if r.URL.Query().Get("timestamps") != "1" || r.URL.Query().Get("follow") != "1" {
				t.Error("daemon record identities unavailable", r.URL.RawQuery)
			}
			data := []byte("2026-10-17T00:00:00.000000001Z anchor\n")
			var h [8]byte
			h[0] = 1
			binary.BigEndian.PutUint32(h[4:], uint32(len(data)))
			w.Write(h[:])
			w.Write(data)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/attach") {
			if r.URL.Query().Get("stdin") != "1" || r.Header.Get("Upgrade") != "tcp" {
				t.Error("stdin release was not a Docker upgrade")
			}
			conn, buf, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			defer conn.Close()
			fmt.Fprint(buf, "HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			buf.Flush()
			line, e := buf.ReadString('\n')
			if e != nil {
				t.Error(e)
			}
			released <- line
			return
		}
		http.Error(w, "unexpected", 400)
	}))
	defer server.Close()
	client := server.Client()
	client.Transport = roundTripTarget{server.URL, client.Transport}
	d := &Docker{HTTP: client, prefix: "/v1.45"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, e := d.AttachTimestamped(ctx, "fixed")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(stream)
	stream.Close()
	if e != nil || !strings.Contains(string(raw), "000000001Z anchor") {
		t.Fatal(e, string(raw))
	}
	if e = d.ReleaseOutput(ctx, "fixed"); e != nil {
		t.Fatal(e)
	}
	select {
	case line := <-released:
		if line != "go\n" {
			t.Fatal(line)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	legacy := containerConfig(Job{Command: "untrusted", Workspace: "/workspace"})
	paged := containerConfig(Job{Command: "untrusted", Workspace: "/workspace", OutputAnchor: "marker"})
	legacyHost, pagedHost := legacy["HostConfig"].(map[string]any), paged["HostConfig"].(map[string]any)
	legacyLogs, pagedLogs := legacyHost["LogConfig"].(map[string]any), pagedHost["LogConfig"].(map[string]any)
	if legacyLogs["Type"] != "json-file" || legacyLogs["Config"].(map[string]string)["max-size"] != "128k" || pagedLogs["Type"] != "local" || pagedLogs["Config"].(map[string]string)["max-size"] != "16m" || pagedLogs["Config"].(map[string]string)["max-file"] != "1" {
		t.Fatal("output replay logs are not explicitly bounded")
	}
	delete(legacyHost, "LogConfig")
	delete(pagedHost, "LogConfig")
	if !reflect.DeepEqual(legacyHost, pagedHost) || paged["OpenStdin"] != true {
		t.Fatal("output paging changed child resource/isolation controls")
	}
	cmd := paged["Cmd"].([]string)
	if !strings.Contains(cmd[2], "</dev/null") || !strings.Contains(cmd[2], "read -r") {
		t.Fatal("release could feed the command")
	}
}
