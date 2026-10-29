package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"teamie/services/sandbox/internal/domain"
)

const OutputRetention = domain.OutputRetention
const DurableOutputLimit int64 = domain.DurableOutputLimit
const SharedOutputLimit int64 = domain.SharedOutputLimit
const outputChunkBytes = domain.OutputChunkBytes
const maxOutputFrames = domain.MaxOutputFrames

var errOutputExpired = domain.ErrOutputExpired
var errOutputLimit = domain.ErrOutputLimit
var errOutputGap = domain.ErrOutputGap

type spoolRecord = domain.SpoolRecord
type outputSegment = domain.OutputSegment

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
		file, err := domain.DefaultWorkspaceStorage.OpenNoFollowFile(s.TaskPath(key), os.O_RDONLY, 0)
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

func newOutputAnchor() (string, error) {
	return domain.NewOutputAnchor()
}
func (s *Service) OutputPath(key string) string { return filepath.Join(s.StateRoot, key+".output") }
func (s *Service) openSpool(key string, flag int) (*os.File, error) {
	f, e := domain.DefaultWorkspaceStorage.OpenNoFollowFile(s.OutputPath(key), flag, 0600)
	if e != nil {
		return nil, e
	}
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("invalid output spool")
	}
	return f, nil
}
func encodeSpool(r spoolRecord) []byte {
	return domain.EncodeSpool(r)
}
func readSpoolRecord(r io.Reader) (spoolRecord, error) {
	return domain.ReadSpoolRecord(r)
}
func (s *Service) OutputUsedLocked() (int64, error) {
	entries, e := os.ReadDir(s.StateRoot)
	if e != nil {
		return 0, e
	}
	if e = s.collectExpiredOutputLocked(entries); e != nil {
		return 0, e
	}
	var total int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".output") {
			continue
		}
		i, e := entry.Info()
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || !i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("invalid output spool")
		}
		total += i.Size()
		if total > SharedOutputLimit {
			return total, errOutputLimit
		}
	}
	return total, nil
}

// AppendOutputLocked appends and fsyncs a spool record under TaskMu.
func (s *Service) AppendOutputLocked(key string, t *taskReceipt, r spoolRecord) error {
	if t.OutputStatus == "limited" {
		return errOutputLimit
	}
	used, e := s.OutputUsedLocked()
	if e != nil && !errors.Is(e, errOutputLimit) {
		return e
	}
	keep := int64(len(r.Data))
	remaining := DurableOutputLimit - t.OutputBytes
	overhead := int64(10 + len(r.Timestamp))
	shared := SharedOutputLimit - used - overhead
	if shared < remaining {
		remaining = shared
	}
	if t.OutputFrames >= maxOutputFrames {
		remaining = 0
	}
	if !diskAvailable(s.StateRoot) {
		return errors.New("output disk unavailable")
	}
	if remaining < 0 {
		remaining = 0
	}
	limited := keep > remaining
	if limited {
		keep = remaining
	}
	if keep > 0 {
		r.Data = r.Data[:keep]
		wire := encodeSpool(r)
		f, e := s.openSpool(key, os.O_CREATE|os.O_RDWR)
		if e != nil {
			return e
		}
		i, e := f.Stat()
		if e == nil && i.Size() != t.OutputFileBytes {
			e = errors.New("output spool offset changed")
		}
		if e == nil {
			_, e = f.WriteAt(wire, t.OutputFileBytes)
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return e
		}
		t.OutputFileBytes += int64(len(wire))
		t.OutputBytes += keep
		t.OutputFrames++
		preview := OutputLimit - len(t.Result.Stdout) - len(t.Result.Stderr)
		if preview > len(r.Data) {
			preview = len(r.Data)
		}
		if preview > 0 {
			if r.Stream == 1 {
				t.Result.Stdout += string(r.Data[:preview])
			} else {
				t.Result.Stderr += string(r.Data[:preview])
			}
		}
	}
	if limited {
		t.OutputStatus = "limited"
		t.Result.Truncated = true
		t.CancelRequested = true
		t.CancelCode = "execution_output_limit"
	}
	if e = s.SaveTaskLocked(key, t); e != nil {
		return e
	}
	if limited {
		return errOutputLimit
	}
	return nil
}

func (s *Service) LoadSpoolLocked(key string, t *taskReceipt) error {
	if t.OutputBytes < 0 || t.OutputBytes > DurableOutputLimit || t.OutputFileBytes < 0 || t.OutputFileBytes > SharedOutputLimit || t.OutputFrames < 0 || t.OutputFrames > maxOutputFrames {
		return errors.New("invalid output receipt")
	}
	if t.OutputExpiredAt != nil {
		return s.expireOutputFileLocked(key, t)
	}
	f, e := s.openSpool(key, os.O_RDWR)
	if os.IsNotExist(e) && t.OutputFileBytes == 0 {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || i.Size() < t.OutputFileBytes {
		return errors.New("output spool missing committed bytes")
	}
	if i.Size() > t.OutputFileBytes {
		if e = f.Truncate(t.OutputFileBytes); e != nil {
			return e
		}
		if e = f.Sync(); e != nil {
			return e
		}
	}
	r := io.LimitReader(f, t.OutputFileBytes)
	var size int64
	frames := 0
	var out, errout strings.Builder
	for {
		record, e := readSpoolRecord(r)
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		size += int64(len(record.Data))
		frames++
		remaining := OutputLimit - out.Len() - errout.Len()
		if remaining > len(record.Data) {
			remaining = len(record.Data)
		}
		if remaining > 0 {
			if record.Stream == 1 {
				out.Write(record.Data[:remaining])
			} else {
				errout.Write(record.Data[:remaining])
			}
		}
	}
	if size != t.OutputBytes || frames != t.OutputFrames {
		return errors.New("invalid output spool length")
	}
	t.Result.Stdout, t.Result.Stderr = out.String(), errout.String()
	return nil
}

func (s *Service) readSpoolLocked(key string, t *taskReceipt, offset int64, limit int32) (string, string, int64, error) {
	if t.OutputExpiredAt != nil {
		return "", "", offset, errOutputExpired
	}
	if offset < 0 || offset > t.OutputBytes {
		return "", "", offset, errors.New("invalid output offset")
	}
	if t.OutputBytes == 0 {
		return "", "", offset, nil
	}
	f, e := s.openSpool(key, os.O_RDONLY)
	if e != nil {
		return "", "", offset, e
	}
	defer f.Close()
	r := io.LimitReader(f, t.OutputFileBytes)
	var out, errout strings.Builder
	position := int64(0)
	remaining := int64(limit)
	for remaining > 0 {
		record, e := readSpoolRecord(r)
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", "", offset, e
		}
		end := position + int64(len(record.Data))
		if end > offset {
			start := int64(0)
			if offset > position {
				start = offset - position
			}
			take := int64(len(record.Data)) - start
			if take > remaining {
				take = remaining
			}
			if record.Stream == 1 {
				out.Write(record.Data[start : start+take])
			} else {
				errout.Write(record.Data[start : start+take])
			}
			remaining -= take
		}
		position = end
	}
	return out.String(), errout.String(), offset + int64(out.Len()+errout.Len()), nil
}

func (s *Service) outputSegmentsLocked(key string, t *taskReceipt, start, end int64) ([]outputSegment, error) {
	if t.OutputExpiredAt != nil {
		return nil, errOutputExpired
	}
	segments := []outputSegment{}
	position := int64(0)
	add := func(stream byte, data []byte) {
		next := position + int64(len(data))
		from, to := position, next
		if from < start {
			from = start
		}
		if to > end {
			to = end
		}
		if to > from {
			name := "stdout"
			if stream == 2 {
				name = "stderr"
			}
			segments = append(segments, outputSegment{Stream: name, ByteOffset: from, NextOffset: to, Data: append([]byte(nil), data[from-position:to-position]...)})
		}
		position = next
	}
	if t.Version == 1 {
		parts := t.Output
		if len(parts) == 0 {
			parts = []outputPart{{Stream: 1, Data: t.Result.Stdout}, {Stream: 2, Data: t.Result.Stderr}}
		}
		for _, part := range parts {
			add(part.Stream, []byte(part.Data))
		}
		return segments, nil
	}
	if start == end {
		return segments, nil
	}
	f, e := s.openSpool(key, os.O_RDONLY)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	r := io.LimitReader(f, t.OutputFileBytes)
	for position < end {
		record, e := readSpoolRecord(r)
		if e != nil {
			return nil, e
		}
		add(record.Stream, record.Data)
	}
	return segments, nil
}

func (s *Service) spoolTailLocked(key string, t *taskReceipt) ([]spoolRecord, error) {
	if t.OutputFileBytes == 0 {
		return nil, nil
	}
	f, e := s.openSpool(key, os.O_RDONLY)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	r := io.LimitReader(f, t.OutputFileBytes)
	tail := []spoolRecord{}
	for {
		record, e := readSpoolRecord(r)
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if len(tail) == 32 {
			copy(tail, tail[1:])
			tail = tail[:31]
		}
		tail = append(tail, record)
	}
	return tail, nil
}

func (s *Service) FinishOutputLocked(key string, t *taskReceipt) error {
	if t.OutputStatus != "limited" && t.OutputStatus != "unknown" {
		t.OutputStatus = "complete"
	}
	f, e := s.openSpool(key, os.O_RDONLY)
	if os.IsNotExist(e) && t.OutputFileBytes == 0 {
		empty := sha256.Sum256(nil)
		t.OutputSHA256 = hex.EncodeToString(empty[:])
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, io.LimitReader(f, t.OutputFileBytes)); e != nil {
		return e
	}
	t.OutputSHA256 = hex.EncodeToString(h.Sum(nil))
	return nil
}
