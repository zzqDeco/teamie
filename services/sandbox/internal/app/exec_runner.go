package app

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"time"

	"teamie/services/sandbox/internal/domain"
)

func (s *Service) observeLocked(key string, t *taskReceipt, recovering bool) {
	ctx, cancel := context.WithCancel(context.Background())
	s.observers[key] = cancel
	s.taskWG.Add(1)
	go func() {
		defer s.taskWG.Done()
		defer func() { s.TaskMu.Lock(); delete(s.observers, key); s.TaskMu.Unlock(); <-s.slots }()
		s.supervise(ctx, key, t, recovering)
	}()
}

func (s *Service) setUnknown(key string, t *taskReceipt) {
	s.TaskMu.Lock()
	defer s.TaskMu.Unlock()
	t.State = "unknown"
	t.Result.ErrorCode = "execution_unknown"
	t.Result.ExitCode = -1
	if t.Version == 2 {
		t.OutputStatus = "unknown"
		t.Result.Truncated = true
	}
	_ = s.SaveTaskLocked(key, t)
}

func (s *Service) inspect(ctx context.Context, m ManagedEngine, name string) (ContainerState, error) {
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.Inspect(op, name)
}

func (s *Service) supervise(ctx context.Context, key string, t *taskReceipt, recovering bool) {
	s.TaskMu.Lock()
	agentIDForLog, runIDForLog, taskIDForLog := t.AgentID, t.RunID, t.TaskID
	hook := s.SuperviseHook
	s.TaskMu.Unlock()
	var endHook func(state, code string, outputBytes int64, ctxErr error)
	if hook != nil {
		ctx, endHook = hook(ctx, agentIDForLog, runIDForLog, taskIDForLog, recovering)
	}
	defer func() {
		if endHook == nil {
			return
		}
		s.TaskMu.Lock()
		state, code, bytes := t.State, t.Result.ErrorCode, t.OutputBytes
		s.TaskMu.Unlock()
		endHook(state, code, bytes, ctx.Err())
	}()
	m := s.Engine.(ManagedEngine)
	s.TaskMu.Lock()
	name, agentID, command, state, version, anchor := t.ContainerName, t.AgentID, t.Command, t.State, t.Version, t.OutputAnchor
	s.TaskMu.Unlock()
	obj, e := s.inspect(ctx, m, name)
	if errors.Is(e, errContainerMissing) && state == "not_started" {
		workspace, workspaceErr := s.WorkspaceDir(agentID)
		if workspaceErr != nil {
			s.setUnknown(key, t)
			return
		}
		op, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, e = m.Create(op, Job{Name: name, Image: s.Image, Workspace: workspace, AgentHash: digest(agentID), TaskHash: key, Command: command, OutputAnchor: anchor})
		cancel()
		obj, e = s.inspect(ctx, m, name)
	}
	if e != nil {
		if ctx.Err() == nil {
			if errors.Is(e, errContainerMissing) {
				s.TaskMu.Lock()
				t.ContainerID = ""
				s.TaskMu.Unlock()
			}
			s.setUnknown(key, t)
		}
		return
	}
	if obj.Config.Labels[ManagedLabel] != "true" || obj.Config.Labels["io.teamie.agent"] != digest(agentID) || obj.Config.Labels["io.teamie.task"] != key {
		s.setUnknown(key, t)
		return
	}
	s.TaskMu.Lock()
	t.ContainerID = obj.ID
	s.TaskMu.Unlock()
	var stream io.ReadCloser
	if obj.State.Status == "created" {
		s.TaskMu.Lock()
		canceled := t.CancelRequested
		s.TaskMu.Unlock()
		if !canceled {
			if version == 1 {
				stream, e = m.Attach(ctx, obj.ID, false)
			}
			if e != nil {
				if ctx.Err() == nil {
					s.setUnknown(key, t)
				}
				return
			}
			s.TaskMu.Lock()
			t.State = "running"
			if t.Deadline.IsZero() && t.TimeoutSeconds > 0 {
				t.Deadline = time.Now().UTC().Add(time.Duration(t.TimeoutSeconds) * time.Second)
			}
			e = s.SaveTaskLocked(key, t)
			s.TaskMu.Unlock()
			if e != nil {
				if stream != nil {
					stream.Close()
				}
				s.setUnknown(key, t)
				return
			}
			op, cancel := context.WithTimeout(ctx, 10*time.Second)
			e = m.StartContainer(op, obj.ID)
			cancel()
			if e != nil {
				if stream != nil {
					stream.Close()
				}
				if ctx.Err() == nil {
					s.setUnknown(key, t)
				}
				return
			}
			obj.State.Status = "running"
			obj.State.Running = true
		}
	}
	if stream == nil && obj.State.Status != "created" {
		if version == 2 {
			if timestamped, ok := m.(TimestampedEngine); ok {
				stream, e = timestamped.AttachTimestamped(ctx, obj.ID)
			} else {
				e = errors.New("timestamped recovery unavailable")
			}
		} else {
			stream, e = m.Attach(ctx, obj.ID, true)
		}
		if e != nil {
			if ctx.Err() == nil {
				s.setUnknown(key, t)
			}
			return
		}
	}
	s.TaskMu.Lock()
	if !t.CancelRequested {
		t.State = "running"
		t.Result.ErrorCode = ""
	}
	_ = s.SaveTaskLocked(key, t)
	s.TaskMu.Unlock()
	type capture struct {
		result Result
		err    error
	}
	captured := make(chan capture, 1)
	if stream != nil {
		captureStopped := make(chan struct{})
		captureCtx, cancelCapture := context.WithCancel(ctx)
		defer func() { cancelCapture(); stream.Close(); <-captureStopped }()
		go func() {
			defer close(captureStopped)
			if version == 2 {
				e := s.observeSpool(captureCtx, key, t, stream, m.(TimestampedEngine), obj.ID, recovering)
				if errors.Is(e, errOutputLimit) {
					e = nil
				}
				captured <- capture{err: e}
				return
			}
			s.TaskMu.Lock()
			priorValue, priorStreams := flattenOutput(t.Output)
			s.TaskMu.Unlock()
			result, e := domain.CaptureFrames(stream, func(result Result, parts []outputPart) error {
				s.TaskMu.Lock()
				defer s.TaskMu.Unlock()
				if recovering {
					value, streams := flattenOutput(parts)
					common := len(value)
					if common > len(priorValue) {
						common = len(priorValue)
					}
					if value[:common] != priorValue[:common] || streams[:common] != priorStreams[:common] {
						return errors.New("container log recovery prefix changed")
					}
				}
				if len(result.Stdout)+len(result.Stderr) < len(t.Result.Stdout)+len(t.Result.Stderr) {
					return nil
				}
				t.Result.Stdout, t.Result.Stderr, t.Result.Truncated = result.Stdout, result.Stderr, result.Truncated
				t.Output = append([]outputPart(nil), parts...)
				if result.Truncated {
					t.CancelRequested = true
					t.CancelCode = "execution_output_limit"
				}
				return s.SaveTaskLocked(key, t)
			})
			captured <- capture{result, e}
		}()
	} else {
		captured <- capture{}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var output *capture
	killSent := false
	killEffective := false
	for {
		select {
		case <-ctx.Done():
			return
		case cap := <-captured:
			output = &cap
			captured = nil
			if cap.err != nil && ctx.Err() == nil {
				op, cancel := operationContext()
				_ = m.Kill(op, obj.ID)
				cancel()
				s.setUnknown(key, t)
				return
			}
		case <-ticker.C:
		}
		s.TaskMu.Lock()
		cancelRequested := t.CancelRequested
		deadline := t.Deadline
		s.TaskMu.Unlock()
		if !cancelRequested && !deadline.IsZero() && !time.Now().Before(deadline) {
			s.TaskMu.Lock()
			t.CancelRequested = true
			t.CancelCode = "execution_timeout"
			_ = s.SaveTaskLocked(key, t)
			s.TaskMu.Unlock()
			cancelRequested = true
		}
		if !cancelRequested && (!diskAvailable(s.WorkspaceRoot) || !workspaceWithinQuota(filepath.Join(s.WorkspaceRoot, digest(agentID)))) {
			s.TaskMu.Lock()
			t.CancelRequested = true
			t.CancelCode = "execution_disk_limit"
			_ = s.SaveTaskLocked(key, t)
			s.TaskMu.Unlock()
			cancelRequested = true
		}
		if cancelRequested && !killSent {
			if obj.State.Status != "created" {
				op, cancel := context.WithTimeout(ctx, 5*time.Second)
				e = m.Kill(op, obj.ID)
				cancel()
				killEffective = e == nil
				if e != nil {
					obj, e = s.inspect(ctx, m, name)
					if e != nil || obj.State.Running {
						if ctx.Err() == nil {
							s.setUnknown(key, t)
						}
						return
					}
				}
			}
			killSent = true
		}
		obj, e = s.inspect(ctx, m, name)
		if e != nil {
			if ctx.Err() == nil {
				s.setUnknown(key, t)
			}
			return
		}
		if obj.State.Running {
			continue
		}
		if obj.State.Status == "created" && !cancelRequested {
			continue
		}
		if output == nil {
			continue
		}
		s.TaskMu.Lock()
		t.Result.ExitCode = obj.State.ExitCode
		t.Result.ErrorCode = ""
		t.State = "completed"
		if obj.State.ExitCode != 0 || obj.State.OOMKilled || obj.State.Error != "" {
			t.State = "failed"
		}
		if t.CancelRequested && (killEffective || obj.State.Status == "created" || t.CancelCode != "execution_canceled") {
			t.State = "canceled"
			t.Result.ExitCode = -1
			t.Result.ErrorCode = t.CancelCode
			if t.CancelCode != "execution_canceled" {
				t.State = "failed"
			}
		}
		if version == 2 {
			e = s.FinishOutputLocked(key, t)
		}
		if e == nil {
			e = s.SaveTaskLocked(key, t)
		}
		s.TaskMu.Unlock()
		if e != nil {
			s.setUnknown(key, t)
			return
		}
		if e == nil {
			op, cancel := operationContext()
			if m.Remove(op, obj.ID) == nil {
				s.TaskMu.Lock()
				t.CleanupDone = true
				_ = s.SaveTaskLocked(key, t)
				_ = s.RetainOutputLocked(key, t, false)
				s.TaskMu.Unlock()
			}
			cancel()
		}
		return
	}
}

func (s *Service) captureSpool(ctx context.Context, key string, t *taskReceipt, stream io.Reader, m TimestampedEngine, id string, recovering bool) error {
	s.TaskMu.Lock()
	tail, e := s.spoolTailLocked(key, t)
	anchorTime := t.OutputAnchorTime
	anchor := t.OutputAnchor
	s.TaskMu.Unlock()
	if e != nil {
		return e
	}
	if len(tail) == 0 && anchorTime != "" {
		tail = []spoolRecord{{Stream: 1, Timestamp: anchorTime, Chunk: 0, Data: []byte(anchor + "\n")}}
	}
	if recovering && len(tail) == 0 && anchorTime == "" {
		recovering = false
	}
	matched := !recovering
	overlap := -1
	first := true
	released := false
	release := func() error {
		if released {
			return nil
		}
		released = true
		s.TaskMu.Lock()
		alreadyReleased := t.OutputGateReleased || t.OutputBytes > 0
		s.TaskMu.Unlock()
		if alreadyReleased {
			return nil
		}
		op, cancel := operationContext()
		defer cancel()
		e := m.ReleaseOutput(op, id)
		if e != nil {
			obj, ie := s.inspect(ctx, s.Engine.(ManagedEngine), id)
			if ie == nil && !obj.State.Running {
				return nil
			}
		}
		if e == nil {
			s.TaskMu.Lock()
			t.OutputGateReleased = true
			e = s.SaveTaskLocked(key, t)
			s.TaskMu.Unlock()
		}
		return e
	}
	var h [8]byte
	for {
		_, e := io.ReadFull(stream, h[:])
		if e == io.EOF {
			if recovering && !matched || first && !recovering {
				return errOutputGap
			}
			return nil
		}
		if e != nil {
			return e
		}
		if (h[0] != 1 && h[0] != 2) || h[1] != 0 || h[2] != 0 || h[3] != 0 {
			return errors.New("invalid container stream")
		}
		size := int64(binary.BigEndian.Uint32(h[4:]))
		if size > 16*1024*1024 {
			return errors.New("container output frame exceeded limits")
		}
		frame := &io.LimitedReader{R: stream, N: size}
		stamp := make([]byte, 0, 64)
		for {
			var ch [1]byte
			if _, e = io.ReadFull(frame, ch[:]); e != nil {
				return errors.New("missing daemon timestamp")
			}
			if ch[0] == ' ' {
				break
			}
			stamp = append(stamp, ch[0])
			if len(stamp) > 64 {
				return errors.New("invalid daemon timestamp")
			}
		}
		stampTime, e := time.Parse(time.RFC3339Nano, string(stamp))
		if e != nil {
			return errors.New("invalid daemon timestamp")
		}
		chunk := uint32(0)
		for {
			data := make([]byte, outputChunkBytes)
			n, e := io.ReadFull(frame, data)
			if e == io.EOF {
				if frame.N != 0 {
					return io.ErrUnexpectedEOF
				}
				break
			}
			if e != nil && e != io.ErrUnexpectedEOF {
				return e
			}
			if e == io.ErrUnexpectedEOF && frame.N != 0 {
				return e
			}
			data = data[:n]
			record := spoolRecord{Stream: h[0], Timestamp: string(stamp), Chunk: chunk, Data: data}
			chunk++
			if first && !recovering {
				first = false
				if record.Stream != 1 || string(data) != anchor+"\n" {
					return errOutputGap
				}
				s.TaskMu.Lock()
				t.OutputAnchorTime = record.Timestamp
				e = s.SaveTaskLocked(key, t)
				s.TaskMu.Unlock()
				if e != nil {
					return e
				}
				if e = release(); e != nil {
					return e
				}
				continue
			}
			if recovering && !matched {
				if overlap < 0 {
					for i := range tail {
						if record.Timestamp == tail[i].Timestamp && record.Stream == tail[i].Stream && record.Chunk == tail[i].Chunk && record.Identity() != tail[i].Identity() {
							return errOutputGap
						}
						if record.Identity() == tail[i].Identity() {
							overlap = i
							break
						}
					}
				} else {
					overlap++
					if overlap >= len(tail) || record.Identity() != tail[overlap].Identity() {
						return errOutputGap
					}
				}
				if overlap == len(tail)-1 && overlap >= 0 {
					matched = true
					if e = release(); e != nil {
						return e
					}
					continue
				}
				if overlap < 0 && len(tail) > 0 {
					lastTime, _ := time.Parse(time.RFC3339Nano, tail[len(tail)-1].Timestamp)
					if stampTime.After(lastTime) {
						return errOutputGap
					}
				}
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.TaskMu.Lock()
			e = s.AppendOutputLocked(key, t, record)
			s.TaskMu.Unlock()
			if e != nil {
				return e
			}
		}
	}
}

func (s *Service) observeSpool(ctx context.Context, key string, t *taskReceipt, stream io.ReadCloser, m TimestampedEngine, id string, recovering bool) error {
	current := stream
	defer func() {
		if current != nil {
			current.Close()
		}
	}()
	for {
		if e := s.captureSpool(ctx, key, t, current, m, id, recovering); e != nil {
			return e
		}
		obj, e := s.inspect(ctx, s.Engine.(ManagedEngine), id)
		if e != nil {
			return e
		}
		if !obj.State.Running {
			return nil
		}
		current.Close()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		current, e = m.AttachTimestamped(ctx, id)
		if e != nil {
			return e
		}
		recovering = true
	}
}
