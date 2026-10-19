// Package sandbox is the only application component allowed to contact Docker.
package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"teamie/backend/pkg/observability"
	"time"
)

const OutputLimit = 65536
const MemoryLimit = 128 * 1024 * 1024
const ManagedLabel = "io.teamie.sandbox"

type Job struct {
	Name, Image, Workspace, AgentHash, Command string
	TaskHash                                   string
	Timeout                                    int
	OutputAnchor                               string
}
type Result struct {
	ExitCode  int32  `json:"exitCode"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
	ErrorCode string `json:"errorCode,omitempty"`
}
type Engine interface {
	Ready(context.Context, string) error
	Cleanup(context.Context) error
	Run(context.Context, Job) (Result, error)
}
type Docker struct {
	HTTP   *http.Client
	prefix string
}

func NewDocker(socket string) *Docker {
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}
	return &Docker{HTTP: &http.Client{Transport: observability.Transport{Base: tr, Peer: "docker", Quiet: true}}, prefix: "/v1.45"}
}
func (d *Docker) request(ctx context.Context, method, path string, data any) (*http.Response, error) {
	var body io.Reader
	if data != nil {
		b, e := json.Marshal(data)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://docker"+d.prefix+path, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.Contains(path, "/attach?") {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "tcp")
	}
	res, e := d.HTTP.Do(req)
	if e != nil {
		return nil, errors.New("Docker API unavailable")
	}
	if res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("Docker API returned status %d", res.StatusCode)
	}
	return res, nil
}
func (d *Docker) Ready(ctx context.Context, image string) error {
	res, e := d.request(ctx, "GET", "/images/"+url.PathEscape(image)+"/json", nil)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	var obj struct{ ID string }
	if json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&obj) != nil || obj.ID == "" {
		return errors.New("runner image unavailable")
	}
	return nil
}
func (d *Docker) Cleanup(ctx context.Context) error {
	q := url.Values{"all": {"true"}, "filters": {"{\"label\":[\"" + ManagedLabel + "=true\"]}"}}
	res, e := d.request(ctx, "GET", "/containers/json?"+q.Encode(), nil)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	var list []struct{ ID string }
	if json.NewDecoder(io.LimitReader(res.Body, 1048576)).Decode(&list) != nil {
		return errors.New("invalid Docker container listing")
	}
	for _, c := range list {
		if e = d.remove(ctx, c.ID); e != nil {
			return e
		}
	}
	return nil
}
func (d *Docker) remove(ctx context.Context, id string) error {
	res, e := d.request(ctx, "DELETE", "/containers/"+id+"?force=true&v=true", nil)
	if e == nil {
		res.Body.Close()
	}
	return e
}
func legacyContainerConfig(j Job) map[string]any {
	return map[string]any{"Image": j.Image, "User": "10001:10001", "WorkingDir": "/workspace", "Cmd": []string{"/bin/sh", "-c", j.Command}, "Env": []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/workspace", "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1"}, "AttachStdout": true, "AttachStderr": true, "Tty": false, "OpenStdin": false, "Labels": map[string]string{ManagedLabel: "true", "io.teamie.agent": j.AgentHash, "io.teamie.task": j.TaskHash}, "StopTimeout": 1, "HostConfig": map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": MemoryLimit, "MemorySwap": MemoryLimit, "NanoCpus": 500000000, "PidsLimit": 32, "OomKillDisable": false, "Init": true, "AutoRemove": false, "Binds": []string{j.Workspace + ":/workspace:rw,rprivate"}, "Tmpfs": map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=16m,mode=1777"}, "Ulimits": []map[string]any{{"Name": "nofile", "Soft": 128, "Hard": 128}, {"Name": "fsize", "Soft": 8 * 1024 * 1024, "Hard": 8 * 1024 * 1024}}, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "128k", "max-file": "1"}}}}
}
func containerConfig(j Job) map[string]any {
	config := legacyContainerConfig(j)
	if j.OutputAnchor != "" {
		// The user command cannot run before a daemon timestamp anchor is
		// durable. Its stdin is closed before exec, so duplicate release after
		// an ambiguous transport/crash cannot become command input.
		config["Cmd"] = []string{"/bin/sh", "-c", `printf '%s\n' "$1"; IFS= read -r release; exec /bin/sh -c "$2" </dev/null`, "teamie-output", j.OutputAnchor, j.Command}
		config["OpenStdin"] = true
		// json-file replaces invalid UTF-8 bytes before the logs API can read
		// them. The built-in local driver retains bytes in protobuf records,
		// including daemon timestamps, within one bounded replay log.
		// Moby jsonlogbytes.go replaces invalid UTF-8 with U+FFFD; local.go's
		// messageToProto/protoToMessage instead copies Line bytes unchanged:
		// https://github.com/moby/moby/blob/master/daemon/logger/jsonfilelog/jsonlog/jsonlogbytes.go
		// https://github.com/moby/moby/blob/master/daemon/logger/local/local.go
		config["HostConfig"].(map[string]any)["LogConfig"] = map[string]any{"Type": "local", "Config": map[string]string{"max-size": "16m", "max-file": "1", "compress": "false"}}
	}
	return config
}
func (d *Docker) Run(ctx context.Context, j Job) (result Result, err error) {
	res, e := d.request(ctx, "POST", "/containers/create?name="+url.QueryEscape(j.Name), containerConfig(j))
	if e != nil {
		return result, e
	}
	var created struct{ ID string }
	e = json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&created)
	res.Body.Close()
	if e != nil || created.ID == "" {
		return result, errors.New("container creation failed")
	}
	cleanupFailed := false
	// Cancellation can interrupt response BODY reads after Docker sent HTTP 200.
	// Classify every post-create path, while failed cleanup remains unknown.
	defer func() {
		if ctx.Err() != nil && !cleanupFailed {
			result.ExitCode = -1
			result.ErrorCode = "execution_canceled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.ErrorCode = "execution_timeout"
			}
			err = nil
		}
	}()
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := d.remove(clean, created.ID); e != nil {
			cleanupFailed = true
			err = errors.New("container cleanup failed")
		}
	}()
	// Attach before start: no fast process can outrun collection or rotated logs.
	stream, e := d.request(ctx, "POST", "/containers/"+created.ID+"/attach?logs=0&stream=1&stdout=1&stderr=1", nil)
	if e != nil {
		return result, e
	}
	defer stream.Body.Close()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			stream.Body.Close()
		case <-watchDone:
		}
	}()
	type captured struct {
		out, errout string
		truncated   bool
		err         error
	}
	output := make(chan captured, 1)
	go func() {
		out, errout, truncated, e := demux(stream.Body, OutputLimit)
		stream.Body.Close()
		if truncated {
			killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			k, killErr := d.request(killCtx, "POST", "/containers/"+created.ID+"/kill?signal=KILL", nil)
			if killErr == nil {
				k.Body.Close()
			}
			cancel()
		}
		output <- captured{out, errout, truncated, e}
	}()
	res, e = d.request(ctx, "POST", "/containers/"+created.ID+"/start", nil)
	if e != nil {
		stream.Body.Close()
		<-output
		return result, e
	}
	res.Body.Close()
	res, e = d.request(ctx, "POST", "/containers/"+created.ID+"/wait?condition=not-running", nil)
	if e != nil {
		stream.Body.Close()
		logs := <-output
		result.Stdout, result.Stderr, result.Truncated = logs.out, logs.errout, logs.truncated
		if ctx.Err() != nil {
			result.ExitCode = -1
			result.ErrorCode = "execution_canceled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.ErrorCode = "execution_timeout"
			}
			return result, nil
		}
		return result, e
	}
	var wait struct {
		StatusCode int32
		Error      *struct{ Message string }
	}
	e = json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&wait)
	res.Body.Close()
	if e != nil || wait.Error != nil {
		stream.Body.Close()
		<-output
		return result, errors.New("container wait failed")
	}
	logs := <-output
	result.ExitCode = wait.StatusCode
	result.Stdout, result.Stderr, result.Truncated = logs.out, logs.errout, logs.truncated
	if logs.err != nil {
		return result, errors.New("container output collection failed")
	}
	return result, nil
}

// Docker's non-TTY output has an eight-byte header per stdout/stderr frame.
func demux(r io.Reader, limit int) (string, string, bool, error) {
	var out, errout strings.Builder
	total := 0
	truncated := false
	var header [8]byte
	for {
		_, e := io.ReadFull(r, header[:])
		if e == io.EOF {
			return out.String(), errout.String(), truncated, nil
		}
		if e != nil {
			return out.String(), errout.String(), truncated, errors.New("invalid container output")
		}
		if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return out.String(), errout.String(), truncated, errors.New("invalid container output stream")
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if size > 16*1024*1024 {
			return out.String(), errout.String(), truncated, errors.New("container output frame exceeded limits")
		}
		keep := int64(limit - total)
		if keep > size {
			keep = size
		}
		if keep < 0 {
			keep = 0
		}
		b := make([]byte, keep)
		if _, e = io.ReadFull(r, b); e != nil {
			return out.String(), errout.String(), truncated, e
		}
		if header[0] == 1 {
			out.Write(b)
		} else {
			errout.Write(b)
		}
		total += len(b)
		if size > keep {
			return out.String(), errout.String(), true, nil
		}
	}
}
