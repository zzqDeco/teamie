// Package docker implements the Docker Engine outbound port for the Sandbox bounded context.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"teamie/pkg/observability"
	"teamie/services/sandbox/internal/domain"
)

var (
	_ domain.Engine            = (*Docker)(nil)
	_ domain.ManagedEngine     = (*Docker)(nil)
	_ domain.TimestampedEngine = (*Docker)(nil)
)

// Docker implements domain.Engine, domain.ManagedEngine, and domain.TimestampedEngine over the Docker Engine UNIX socket API.
type Docker struct {
	HTTP   *http.Client
	Prefix string
}

// NewWithClient constructs a Docker instance using a custom HTTP client and API prefix.
func NewWithClient(client *http.Client, prefix string) *Docker {
	if prefix == "" {
		prefix = "/v1.45"
	}
	return &Docker{HTTP: client, Prefix: prefix}
}

// NewDocker creates a new Docker client connected to the given UNIX socket path.
func NewDocker(socket string) *Docker {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Docker{
		HTTP:   &http.Client{Transport: observability.Transport{Base: tr, Peer: "docker", Quiet: true}},
		Prefix: "/v1.45",
	}
}

func (d *Docker) prefixPath() string {
	if d.Prefix == "" {
		return "/v1.45"
	}
	return d.Prefix
}

// Request executes an HTTP request against the configured Docker Engine socket.
func (d *Docker) Request(ctx context.Context, method, path string, data any) (*http.Response, error) {
	return d.request(ctx, method, path, data)
}

func (d *Docker) request(ctx context.Context, method, path string, data any) (*http.Response, error) {
	var body io.Reader
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+d.prefixPath()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.Contains(path, "/attach?") {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "tcp")
	}
	res, err := d.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("Docker API unavailable")
	}
	if res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("Docker API returned status %d", res.StatusCode)
	}
	return res, nil
}

// Ready verifies that the specified runner image exists locally in Docker.
func (d *Docker) Ready(ctx context.Context, image string) error {
	res, err := d.request(ctx, "GET", "/images/"+url.PathEscape(image)+"/json", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var obj struct{ ID string }
	if json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&obj) != nil || obj.ID == "" {
		return errors.New("runner image unavailable")
	}
	return nil
}

// Cleanup force-removes all containers bearing the ManagedLabel.
func (d *Docker) Cleanup(ctx context.Context) error {
	q := url.Values{"all": {"true"}, "filters": {"{\"label\":[\"" + domain.ManagedLabel + "=true\"]}"}}
	res, err := d.request(ctx, "GET", "/containers/json?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var list []struct{ ID string }
	if json.NewDecoder(io.LimitReader(res.Body, 1048576)).Decode(&list) != nil {
		return errors.New("invalid Docker container listing")
	}
	for _, c := range list {
		if err = d.remove(ctx, c.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d *Docker) remove(ctx context.Context, id string) error {
	res, err := d.request(ctx, "DELETE", "/containers/"+id+"?force=true&v=true", nil)
	if err == nil {
		res.Body.Close()
	}
	return err
}

// LegacyContainerConfig returns the hardened Docker container creation payload without output-anchor gating.
func LegacyContainerConfig(j domain.Job) map[string]any {
	return map[string]any{
		"Image":        j.Image,
		"User":         "10001:10001",
		"WorkingDir":   "/workspace",
		"Cmd":          []string{"/bin/sh", "-c", j.Command},
		"Env":          []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/workspace", "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1"},
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"OpenStdin":    false,
		"Labels":       map[string]string{domain.ManagedLabel: "true", "io.teamie.agent": j.AgentHash, "io.teamie.task": j.TaskHash},
		"StopTimeout":  1,
		"HostConfig": map[string]any{
			"NetworkMode":    "none",
			"ReadonlyRootfs": true,
			"CapDrop":        []string{"ALL"},
			"SecurityOpt":    []string{"no-new-privileges:true"},
			"Memory":         domain.MemoryLimit,
			"MemorySwap":     domain.MemoryLimit,
			"NanoCpus":       500000000,
			"PidsLimit":      32,
			"OomKillDisable": false,
			"Init":           true,
			"AutoRemove":     false,
			"Binds":          []string{j.Workspace + ":/workspace:rw,rprivate"},
			"Tmpfs":          map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=16m,mode=1777"},
			"Ulimits":        []map[string]any{{"Name": "nofile", "Soft": 128, "Hard": 128}, {"Name": "fsize", "Soft": 8 * 1024 * 1024, "Hard": 8 * 1024 * 1024}},
			"LogConfig":      map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "128k", "max-file": "1"}},
		},
	}
}

// ContainerConfig returns the hardened Docker container creation payload, enabling local log driver and stdin gate when OutputAnchor is set.
func ContainerConfig(j domain.Job) map[string]any {
	config := LegacyContainerConfig(j)
	if j.OutputAnchor != "" {
		config["Cmd"] = []string{"/bin/sh", "-c", `printf '%s\n' "$1"; IFS= read -r release; exec /bin/sh -c "$2" </dev/null`, "teamie-output", j.OutputAnchor, j.Command}
		config["OpenStdin"] = true
		config["HostConfig"].(map[string]any)["LogConfig"] = map[string]any{"Type": "local", "Config": map[string]string{"max-size": "16m", "max-file": "1", "compress": "false"}}
	}
	return config
}

// Run creates, attaches, starts, waits, and cleans up a synchronous container execution.
func (d *Docker) Run(ctx context.Context, j domain.Job) (result domain.Result, err error) {
	res, e := d.request(ctx, "POST", "/containers/create?name="+url.QueryEscape(j.Name), ContainerConfig(j))
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
		out, errout, truncated, e := domain.Demux(stream.Body, domain.OutputLimit)
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

// AttachTimestamped opens a timestamped stdout/stderr log follow stream for id.
func (d *Docker) AttachTimestamped(ctx context.Context, id string) (io.ReadCloser, error) {
	res, err := d.request(ctx, "GET", "/containers/"+url.PathEscape(id)+"/logs?follow=1&stdout=1&stderr=1&timestamps=1", nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// ReleaseOutput sends the release line to the container's stdin gate.
func (d *Docker) ReleaseOutput(ctx context.Context, id string) error {
	res, err := d.request(ctx, "POST", "/containers/"+url.PathEscape(id)+"/attach?logs=0&stream=1&stdin=1&stdout=0&stderr=0", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	w, ok := res.Body.(io.Writer)
	if !ok {
		return errors.New("Docker stdin upgrade unavailable")
	}
	_, err = io.WriteString(w, "go\n")
	return err
}

// Create creates a managed container without starting it.
func (d *Docker) Create(ctx context.Context, j domain.Job) (string, error) {
	res, err := d.request(ctx, "POST", "/containers/create?name="+url.QueryEscape(j.Name), ContainerConfig(j))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var obj struct{ ID string }
	if json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&obj) != nil || obj.ID == "" {
		return "", errors.New("container creation failed")
	}
	return obj.ID, nil
}

// StartContainer starts a created container by ID.
func (d *Docker) StartContainer(ctx context.Context, id string) error {
	res, err := d.request(ctx, "POST", "/containers/"+url.PathEscape(id)+"/start", nil)
	if err == nil {
		res.Body.Close()
	}
	return err
}

// Inspect returns the current ContainerState of id, or domain.ErrContainerMissing on 404.
func (d *Docker) Inspect(ctx context.Context, id string) (domain.ContainerState, error) {
	var obj domain.ContainerState
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker"+d.prefixPath()+"/containers/"+url.PathEscape(id)+"/json", nil)
	if err != nil {
		return obj, err
	}
	res, err := d.HTTP.Do(req)
	if err != nil {
		return obj, errors.New("Docker API unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return obj, domain.ErrContainerMissing
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 262144)).Decode(&obj) != nil || obj.ID == "" {
		return obj, errors.New("container inspect failed")
	}
	return obj, nil
}

// Attach attaches to a container's live stream or log stream.
func (d *Docker) Attach(ctx context.Context, id string, logs bool) (io.ReadCloser, error) {
	path := "/containers/" + url.PathEscape(id) + "/attach?logs=0&stream=1&stdout=1&stderr=1"
	if logs {
		path = "/containers/" + url.PathEscape(id) + "/logs?follow=1&stdout=1&stderr=1"
	}
	method := "POST"
	if logs {
		method = "GET"
	}
	res, err := d.request(ctx, method, path, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// Kill sends SIGKILL to container id.
func (d *Docker) Kill(ctx context.Context, id string) error {
	res, err := d.request(ctx, "POST", "/containers/"+url.PathEscape(id)+"/kill?signal=KILL", nil)
	if err == nil {
		res.Body.Close()
	}
	return err
}

// Remove force-removes container id and its anonymous volumes.
func (d *Docker) Remove(ctx context.Context, id string) error {
	return d.remove(ctx, url.PathEscape(id))
}
