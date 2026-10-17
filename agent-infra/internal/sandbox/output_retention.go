package sandbox

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"teamie/agent-protocol/kitex_gen/agent"
)

const OutputRetention = 24 * time.Hour

var errOutputExpired = errors.New("output_expired")

func (s *Service) outputTime() time.Time {
	if s.outputNow != nil {
		return s.outputNow().UTC()
	}
	return time.Now().UTC()
}

func outputRetentionEligible(t *taskReceipt) bool {
	return t.Version == 2 && terminalTask(t.State) && t.State != "unknown" && t.CleanupDone
}

func outputRetentionResponse(t *taskReceipt, out *agent.SandboxTaskResponse) {
	if t.Version != 2 {
		return
	}
	seconds := int64(OutputRetention / time.Second)
	availability := "recording"
	if outputRetentionEligible(t) {
		availability = "retained"
	}
	if t.OutputExpiredAt != nil {
		availability = "expired"
	}
	out.OutputRetentionSeconds, out.OutputAvailability = &seconds, &availability
	if t.OutputExpiresAt != nil {
		value := t.OutputExpiresAt.UTC().Format(time.RFC3339Nano)
		out.OutputExpiresAt = &value
	}
}

// All callers hold taskMu. A tombstone is fsynced before unlinking the bytes;
// replay retains the immutable terminal result and can never rerun a command.
func (s *Service) expireOutputFileLocked(key string, t *taskReceipt) error {
	if !outputRetentionEligible(t) || t.OutputExpiredAt == nil || t.OutputExpiresAt == nil || t.OutputExpiredAt.Before(*t.OutputExpiresAt) {
		return errors.New("invalid expired output receipt")
	}
	info, err := os.Lstat(s.outputPath(key))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid output spool")
		}
		if err = os.Remove(s.outputPath(key)); err != nil {
			return err
		}
	}
	// Repeat the directory sync even if an earlier unlink succeeded but its
	// sync failed or the supervisor stopped before recording success.
	dir, err := os.Open(s.StateRoot)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Service) retainOutputLocked(key string, t *taskReceipt, renew bool) error {
	if t.OutputExpiredAt != nil {
		return s.expireOutputFileLocked(key, t)
	}
	if !outputRetentionEligible(t) {
		return nil
	}
	now := s.outputTime()
	if t.OutputExpiresAt != nil && !now.Before(*t.OutputExpiresAt) {
		next := *t
		next.OutputExpiredAt = &now
		if err := s.saveTaskLocked(key, &next); err != nil {
			return err
		}
		*t = next
		return s.expireOutputFileLocked(key, t)
	}
	expires := now.Add(OutputRetention)
	if t.OutputExpiresAt == nil || renew && expires.After(*t.OutputExpiresAt) {
		next := *t
		next.OutputExpiresAt = &expires
		if err := s.saveTaskLocked(key, &next); err != nil {
			return err
		}
		*t = next
	}
	return nil
}

// Only a receipt matching this exact spool can authorize reclamation. Unknown
// files remain counted against capacity, and live/uncertain tasks stay intact.
func (s *Service) reclaimOutputLocked(key string) error {
	t := s.tasks[key]
	if t == nil {
		file, err := os.OpenFile(s.taskPath(key), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 2*1024*1024 {
			file.Close()
			return errors.New("invalid output task receipt")
		}
		raw, err := io.ReadAll(io.LimitReader(file, 2*1024*1024+1))
		file.Close()
		if err != nil {
			return err
		}
		t = new(taskReceipt)
		if len(raw) > 2*1024*1024 || json.Unmarshal(raw, t) != nil || taskKey(t.AgentID, t.TaskID) != key || !validID(t.AgentID) || !validID(t.TaskID) || t.Version != 1 && t.Version != 2 {
			return errors.New("invalid output task receipt")
		}
	}
	return s.retainOutputLocked(key, t, false)
}

func (s *Service) collectExpiredOutputLocked(entries []os.DirEntry) error {
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".output") {
			if err := s.reclaimOutputLocked(strings.TrimSuffix(entry.Name(), ".output")); err != nil {
				return err
			}
		}
	}
	return nil
}

func expiredOutputResponse(t *taskReceipt) *agent.SandboxTaskResponse {
	out := taskResponse(t)
	code, message, empty, more, truncated := "output_expired", "Retained output has expired; the command was not rerun", "", false, true
	out.Ok = false
	out.ErrorCode, out.ErrorMessage = &code, &message
	out.Stdout, out.Stderr = &empty, &empty
	out.HasMore, out.Truncated = &more, &truncated
	return out
}
