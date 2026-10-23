package app

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"teamie/services/sandbox/internal/domain"
)

const OutputRetention = domain.OutputRetention

var errOutputExpired = domain.ErrOutputExpired

func (s *Service) OutputTime() time.Time {
	if s.OutputNow != nil {
		return s.OutputNow().UTC()
	}
	return time.Now().UTC()
}

func outputRetentionEligible(t *taskReceipt) bool {
	return domain.OutputRetentionEligible(t)
}

func outputRetentionResponse(t *taskReceipt, out *domain.SandboxTaskResponse) {
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

func (s *Service) expireOutputFileLocked(key string, t *taskReceipt) error {
	if !outputRetentionEligible(t) || t.OutputExpiredAt == nil || t.OutputExpiresAt == nil || t.OutputExpiredAt.Before(*t.OutputExpiresAt) {
		return errors.New("invalid expired output receipt")
	}
	info, err := os.Lstat(s.OutputPath(key))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid output spool")
		}
		if err = os.Remove(s.OutputPath(key)); err != nil {
			return err
		}
	}
	dir, err := os.Open(s.StateRoot)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Service) RetainOutputLocked(key string, t *taskReceipt, renew bool) error {
	if t.OutputExpiredAt != nil {
		return s.expireOutputFileLocked(key, t)
	}
	if !outputRetentionEligible(t) {
		return nil
	}
	now := s.OutputTime()
	if t.OutputExpiresAt != nil && !now.Before(*t.OutputExpiresAt) {
		next := *t
		next.OutputExpiredAt = &now
		if err := s.SaveTaskLocked(key, &next); err != nil {
			return err
		}
		*t = next
		return s.expireOutputFileLocked(key, t)
	}
	expires := now.Add(OutputRetention)
	if t.OutputExpiresAt == nil || renew && expires.After(*t.OutputExpiresAt) {
		next := *t
		next.OutputExpiresAt = &expires
		if err := s.SaveTaskLocked(key, &next); err != nil {
			return err
		}
		*t = next
	}
	return nil
}

func (s *Service) reclaimOutputLocked(key string) error {
	t := s.Tasks[key]
	if t == nil {
		file, err := os.OpenFile(s.TaskPath(key), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
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
	return s.RetainOutputLocked(key, t, false)
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

func expiredOutputResponse(t *taskReceipt) *domain.SandboxTaskResponse {
	out := taskResponse(t)
	code, message, empty, more, truncated := "output_expired", "Retained output has expired; the command was not rerun", "", false, true
	out.Ok = false
	out.ErrorCode, out.ErrorMessage = &code, &message
	out.Stdout, out.Stderr = &empty, &empty
	out.HasMore, out.Truncated = &more, &truncated
	return out
}
