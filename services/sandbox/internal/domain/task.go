package domain

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	OutputLimit        = 65536
	MemoryLimit        = 128 * 1024 * 1024
	ManagedLabel       = "io.teamie.sandbox"
	DurableOutputLimit = int64(8 * 1024 * 1024)
	SharedOutputLimit  = int64(64 * 1024 * 1024)
	OutputChunkBytes   = 32768
	MaxOutputFrames    = 32768
	OutputRetention    = 24 * time.Hour
)

// Job describes a container execution specification.
type Job struct {
	Name, Image, Workspace, AgentHash, Command string
	TaskHash                                   string
	Timeout                                    int
	OutputAnchor                               string
}

// Result holds the terminal or partial output and exit status of a sandbox execution.
type Result struct {
	ExitCode  int32  `json:"exitCode"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
	ErrorCode string `json:"errorCode,omitempty"`
}

// ContainerState holds the inspected Docker container status.
type ContainerState struct {
	ID     string `json:"Id"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		ExitCode  int32  `json:"ExitCode"`
		OOMKilled bool   `json:"OOMKilled"`
		Error     string `json:"Error"`
	} `json:"State"`
}

// OutputPart represents a single stdout/stderr frame snapshot.
type OutputPart struct {
	Stream byte   `json:"stream"`
	Data   string `json:"data"`
}

func (p OutputPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Stream byte   `json:"stream"`
		Data   []byte `json:"dataBase64"`
	}{p.Stream, []byte(p.Data)})
}

func (p *OutputPart) UnmarshalJSON(b []byte) error {
	var wire struct {
		Stream byte   `json:"stream"`
		Data   []byte `json:"dataBase64"`
		Legacy string `json:"data"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	p.Stream, p.Data = wire.Stream, string(wire.Data)
	if wire.Data == nil {
		p.Data = wire.Legacy
	}
	return nil
}

// FlattenOutput flattens a slice of OutputPart into concatenated data and stream-id bytes.
func FlattenOutput(parts []OutputPart) (string, string) {
	var values, streams strings.Builder
	for _, part := range parts {
		values.WriteString(part.Data)
		streams.WriteString(strings.Repeat(string([]byte{part.Stream}), len(part.Data)))
	}
	return values.String(), streams.String()
}

// CaptureFrames demultiplexes Docker stream frames and invokes persist after each frame.
func CaptureFrames(r io.Reader, persist func(Result, []OutputPart) error) (result Result, err error) {
	var header [8]byte
	var parts []OutputPart
	for {
		_, e := io.ReadFull(r, header[:])
		if e == io.EOF {
			return result, nil
		}
		if e != nil {
			return result, errors.New("invalid container output")
		}
		if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return result, errors.New("invalid container stream")
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if size > 16*1024*1024 {
			return result, errors.New("container output frame exceeded limits")
		}
		keep := int64(OutputLimit - len(result.Stdout) - len(result.Stderr))
		if keep > size {
			keep = size
		}
		data := make([]byte, keep)
		if _, e = io.ReadFull(r, data); e != nil {
			return result, e
		}
		if header[0] == 1 {
			result.Stdout += string(data)
		} else {
			result.Stderr += string(data)
		}
		if len(data) > 0 {
			parts = append(parts, OutputPart{Stream: header[0], Data: string(data)})
		}
		result.Truncated = size > keep
		if e = persist(result, parts); e != nil {
			return result, e
		}
		if result.Truncated {
			return result, nil
		}
	}
}

// Demux demultiplexes a Docker attach stream up to limit bytes.
func Demux(r io.Reader, limit int) (string, string, bool, error) {
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

// SpoolRecord is a durable timestamped output spool record.
type SpoolRecord struct {
	Stream    byte
	Timestamp string
	Chunk     uint32
	Data      []byte
}

// Identity returns the deduplication identity of the spool record.
func (r SpoolRecord) Identity() string {
	return fmt.Sprintf("%s/%d/%d/%s", r.Timestamp, r.Stream, r.Chunk, Digest(string(r.Data)))
}

// NewOutputAnchor generates a random output anchor token.
func NewOutputAnchor() (string, error) {
	var b [32]byte
	_, err := rand.Read(b[:])
	return "TEAMIE_OUTPUT_ANCHOR:" + hex.EncodeToString(b[:]), err
}

// EncodeSpool encodes a SpoolRecord into its binary disk format.
func EncodeSpool(r SpoolRecord) []byte {
	b := make([]byte, 10+len(r.Timestamp)+len(r.Data))
	b[0] = r.Stream
	b[1] = byte(len(r.Timestamp))
	binary.BigEndian.PutUint32(b[2:6], r.Chunk)
	binary.BigEndian.PutUint32(b[6:10], uint32(len(r.Data)))
	copy(b[10:], r.Timestamp)
	copy(b[10+len(r.Timestamp):], r.Data)
	return b
}

// ReadSpoolRecord decodes a single SpoolRecord from r.
func ReadSpoolRecord(r io.Reader) (SpoolRecord, error) {
	var h [10]byte
	_, err := io.ReadFull(r, h[:])
	if err != nil {
		return SpoolRecord{}, err
	}
	n := binary.BigEndian.Uint32(h[6:])
	if (h[0] != 1 && h[0] != 2) || h[1] > 64 || n > OutputChunkBytes {
		return SpoolRecord{}, errors.New("invalid output spool record")
	}
	b := make([]byte, int(h[1])+int(n))
	if _, err = io.ReadFull(r, b); err != nil {
		return SpoolRecord{}, err
	}
	return SpoolRecord{
		Stream:    h[0],
		Timestamp: string(b[:int(h[1])]),
		Chunk:     binary.BigEndian.Uint32(h[2:6]),
		Data:      b[int(h[1]):],
	}, nil
}

// TaskKey computes the deterministic receipt key for (agentID, taskID).
func TaskKey(agentID, taskID string) string {
	return Digest(agentID + "\x00" + taskID)
}

// TerminalTask reports whether state is a terminal task state.
func TerminalTask(state string) bool {
	return state == "completed" || state == "failed" || state == "canceled" || state == "unknown"
}
