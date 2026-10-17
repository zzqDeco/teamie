package agent_protocol_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/cloudwego/gopkg/protocol/thrift"
	"teamie/agent-protocol/kitex_gen/agent"
)

func TestOutputRetentionOptionalWireCompatibility(t *testing.T) {
	// The pre-retention response has only the three required fields. New
	// readers must accept it and nil optional fields must preserve its bytes.
	legacy, err := hex.DecodeString("020001010b0002000000067461736b2d610b000300000009636f6d706c6574656400")
	if err != nil {
		t.Fatal(err)
	}
	var old agent.SandboxTaskResponse
	if n, err := old.FastRead(legacy); err != nil || n != len(legacy) || !old.Ok || old.TaskId != "task-a" || old.State != "completed" || old.OutputExpiresAt != nil || old.OutputRetentionSeconds != nil || old.OutputAvailability != nil {
		t.Fatalf("legacy response changed: %d %v %+v", n, err, old)
	}
	wire := make([]byte, old.BLength())
	if n := old.FastWrite(wire); n != len(wire) || !bytes.Equal(wire, legacy) {
		t.Fatal("optional fields changed legacy receipt bytes")
	}

	expires, seconds, availability := "2026-10-18T01:02:03.123456789Z", int64(86400), "retained"
	old.OutputExpiresAt, old.OutputRetentionSeconds, old.OutputAvailability = &expires, &seconds, &availability
	wire = make([]byte, old.BLength())
	old.FastWrite(wire)
	var result agent.SandboxTaskResponse
	if n, err := result.FastRead(wire); err != nil || n != len(wire) || result.GetOutputExpiresAt() != expires || result.GetOutputRetentionSeconds() != seconds || result.GetOutputAvailability() != availability {
		t.Fatalf("retention response failed round trip: %d %v %+v", n, err, result)
	}

	// An older Thrift reader skips unknown optional fields 16–18. Keeping its
	// known-field bytes yields the exact original response, including STOP.
	known := []byte{}
	for offset := 0; offset < len(wire); {
		kind, id, n, err := thrift.Binary.ReadFieldBegin(wire[offset:])
		if err != nil {
			t.Fatal(err)
		}
		start := offset
		offset += n
		if kind == thrift.STOP {
			known = append(known, wire[start:offset]...)
			break
		}
		n, err = thrift.Binary.Skip(wire[offset:], kind)
		if err != nil {
			t.Fatal(err)
		}
		offset += n
		if id <= 15 {
			known = append(known, wire[start:offset]...)
		}
	}
	if !bytes.Equal(known, legacy) {
		t.Fatal("new metadata changed fields understood by a legacy reader")
	}
}
