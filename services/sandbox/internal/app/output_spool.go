package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"teamie/services/sandbox/internal/domain"
)

const DurableOutputLimit int64 = domain.DurableOutputLimit
const SharedOutputLimit int64 = domain.SharedOutputLimit
const outputChunkBytes = domain.OutputChunkBytes
const maxOutputFrames = domain.MaxOutputFrames

var errOutputLimit = domain.ErrOutputLimit
var errOutputGap = domain.ErrOutputGap

type spoolRecord = domain.SpoolRecord
type outputSegment = domain.OutputSegment

func newOutputAnchor() (string, error) {
	return domain.NewOutputAnchor()
}
func (s *Service) OutputPath(key string) string { return filepath.Join(s.StateRoot, key+".output") }
func (s *Service) openSpool(key string, flag int) (*os.File, error) {
	f, e := os.OpenFile(s.OutputPath(key), flag|syscall.O_NOFOLLOW, 0600)
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
