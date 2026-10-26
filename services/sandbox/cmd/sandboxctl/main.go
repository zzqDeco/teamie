// sandboxctl exposes private operator acceptance checks, never Agent/browser APIs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"teamie/services/sandbox/internal/domain"
	"teamie/services/sandbox/internal/infra/docker"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	out, err := run(ctx, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox acceptance evidence unavailable")
		os.Exit(1)
	}
	if json.NewEncoder(os.Stdout).Encode(out) != nil {
		os.Exit(1)
	}
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func run(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("missing command")
	}
	f := flag.NewFlagSet("sandboxctl", flag.ContinueOnError)
	agent := f.String("agent", "", "Agent ID")
	runID := f.String("run", "", "Run ID")
	task := f.String("task", "", "Task ID")
	nonce := f.String("nonce", "", "probe ID")
	if err := f.Parse(args[1:]); err != nil {
		return nil, err
	}
	switch args[0] {
	case "task":
		if !domain.ValidID(*agent) || !domain.ValidID(*runID) || !domain.ValidID(*task) {
			return nil, errors.New("invalid scope")
		}
		path := filepath.Join(env("SANDBOX_STATE_ROOT", "/srv/teamie/sandbox-state"), domain.TaskKey(*agent, *task)+".task.json")
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		var rec domain.TaskReceipt
		if err = json.NewDecoder(io.LimitReader(file, 2*1024*1024+1)).Decode(&rec); err != nil {
			return nil, err
		}
		if rec.AgentID != *agent || rec.RunID != *runID || rec.TaskID != *task {
			return nil, errors.New("receipt scope mismatch")
		}
		return map[string]any{"version": 1, "agentId": rec.AgentID, "runId": rec.RunID, "taskId": rec.TaskID, "state": rec.State, "success": rec.State == "completed" && rec.Result.ExitCode == 0 && rec.Result.ErrorCode == ""}, nil
	case "isolation":
		if !domain.ValidID(*nonce) {
			return nil, errors.New("invalid nonce")
		}
		return probe(ctx, *nonce)
	default:
		return nil, errors.New("unknown command")
	}
}

type inspected struct {
	Config struct {
		User string
		Env  []string
	}
	HostConfig struct {
		NetworkMode                string
		ReadonlyRootfs, Privileged bool
		CapDrop, SecurityOpt       []string
		PidMode, IpcMode           string
	}
	Mounts []struct{ Type, Source, Destination string }
}

func isolated(c inspected, workspace string) bool {
	if c.Config.User != "10001:10001" || c.HostConfig.NetworkMode != "none" || !c.HostConfig.ReadonlyRootfs || c.HostConfig.Privileged || c.HostConfig.PidMode == "host" || c.HostConfig.IpcMode == "host" || !slices.Contains(c.HostConfig.CapDrop, "ALL") || !slices.Contains(c.HostConfig.SecurityOpt, "no-new-privileges:true") {
		return false
	}
	allowed := map[string]bool{"PATH": true, "HOME": true, "LANG": true, "GPG_KEY": true, "PYTHON_VERSION": true, "PYTHON_SHA256": true, "PYTHONDONTWRITEBYTECODE": true, "PYTHONUNBUFFERED": true}
	for _, e := range c.Config.Env {
		key, _, _ := strings.Cut(e, "=")
		if !allowed[key] {
			return false
		}
	}
	binds := 0
	for _, m := range c.Mounts {
		if m.Type == "tmpfs" && m.Destination == "/tmp" {
			continue
		}
		if m.Type != "bind" || m.Source != workspace || m.Destination != "/workspace" {
			return false
		}
		binds++
	}
	return binds == 1
}
func probe(ctx context.Context, nonce string) (any, error) {
	root := env("SANDBOX_WORKSPACE_ROOT", "/srv/teamie/sandbox-workspaces")
	workspace, err := os.MkdirTemp(root, "acceptance-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workspace)
	peer, err := os.MkdirTemp(root, "acceptance-peer-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(peer)
	if err = os.WriteFile(filepath.Join(peer, "sentinel"), []byte(nonce), 0600); err != nil {
		return nil, err
	}
	if err = os.Chown(workspace, 10001, 10001); err != nil {
		return nil, err
	}
	// Only fixed probe code and JSON-quoted operator scope enter the child.
	peerJSON, _ := json.Marshal(peer)
	nonceJSON, _ := json.Marshal(nonce)
	command := "python3 - <<'TEAMIE_PROBE'\nimport os, socket\nfrom pathlib import Path\nassert os.getuid()==10001\nassert not any(k.endswith(('_TOKEN','_API_KEY','_PASSWORD')) for k in os.environ)\nassert not Path('/var/run/docker.sock').exists()\nassert not Path(" + string(peerJSON) + ").exists()\nassert set(os.listdir('/sys/class/net')) == {'lo'}\ntry:\n s=socket.socket();s.settimeout(1);s.connect(('192.0.2.1',443));raise AssertionError('network available')\nexcept OSError:\n pass\nPath('/workspace/probe-ok').write_text(" + string(nonceJSON) + ")\nTEAMIE_PROBE\n"
	d := docker.NewDocker(env("DOCKER_SOCKET", "/var/run/docker.sock"))
	id, err := d.Create(ctx, domain.Job{Name: "teamie-acceptance-" + nonce, Image: os.Getenv("SANDBOX_RUNNER_IMAGE"), Workspace: workspace, AgentHash: nonce, Command: command})
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.Remove(cleanup, id)
	}()
	res, err := d.Request(ctx, "GET", "/containers/"+url.PathEscape(id)+"/json", nil)
	if err != nil {
		return nil, err
	}
	var config inspected
	err = json.NewDecoder(io.LimitReader(res.Body, 262144)).Decode(&config)
	res.Body.Close()
	if err != nil || !isolated(config, workspace) {
		return nil, errors.New("isolation configuration rejected")
	}
	if err = d.StartContainer(ctx, id); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, e := d.Inspect(ctx, id)
		if e != nil {
			return nil, e
		}
		if !state.State.Running {
			raw, e := os.ReadFile(filepath.Join(workspace, "probe-ok"))
			if e != nil || state.State.ExitCode != 0 || state.State.OOMKilled || string(raw) != nonce {
				return nil, errors.New("isolation probe failed")
			}
			return map[string]any{"version": 1, "nonce": nonce, "verified": true}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
