package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type replayToolOutput struct {
	name string
	raw  []byte
}

type replayToolPosition struct {
	message, block int
	callID         string
}

type observedToolAttempt struct {
	values    map[string]string
	ambiguous bool
}

// Replay an actual prior wire before Eino counts tokens. HTML escapes in the
// stored encoding/json receipt may be longer than the old serializer's text.
// Representation variants are safe to overlay later only when the pinned .35
// tool-text estimate (len(text)/4) is identical. The pinned IM ToolsNode emits
// one result with one text in each message; observedToolAttempts enforces that
// shape. Ambiguous token counts must
// fail closed rather than move a cached summary to another model step.
func (s *Store) replayToolWire(ctx context.Context, run *Run, callID, name string, raw []byte) ([]byte, error) {
	attempts, err := s.observedToolAttempts(ctx, run, map[string]replayToolOutput{callID: {name: name, raw: raw}})
	if err != nil {
		return nil, err
	}
	var chosen string
	for _, attempt := range attempts {
		if attempt.ambiguous {
			return nil, ErrReplayConflict
		}
		value, found := attempt.values[callID]
		if !found {
			continue
		}
		if chosen != "" && len(chosen)/4 != len(value)/4 {
			return nil, ErrReplayConflict
		}
		chosen = value
	}
	if chosen != "" {
		return []byte(chosen), nil
	}
	return raw, nil
}

// Older IM tools persisted encoding/json output, but returned the live DTO
// through Eino's serializer. A cached Observation.Data was decoded into any,
// changing field order (and sometimes integer precision) on a later attempt.
// Only actual outputs recorded by this Run's prior Session attempts can repair
// that representation difference. Each candidate is one observed attempt, not
// a combination of arbitrary JSON spellings. The caller still checks the full
// native/classic request hash before accepting any cached model response.
func (s *Store) observedToolReplayInputs(ctx context.Context, run *Run, messages []*schema.AgenticMessage) ([][]*schema.AgenticMessage, error) {
	rows, err := s.DB.Query(ctx, `SELECT call_id,name,result FROM runtime_tool_calls WHERE run_id=$1 AND result IS NOT NULL AND name IN ('im_list_conversations','im_list_messages','im_get_message','im_send_message','im_get_attachment')`, run.ID)
	if err != nil {
		return nil, err
	}
	receipts := map[string]replayToolOutput{}
	for rows.Next() {
		var callID string
		var output replayToolOutput
		if err = rows.Scan(&callID, &output.name, &output.raw); err != nil {
			rows.Close()
			return nil, err
		}
		receipts[callID] = output
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(receipts) == 0 {
		return nil, err
	}
	attempts, err := s.observedToolAttempts(ctx, run, receipts)
	if err != nil {
		return nil, err
	}
	var positions []replayToolPosition
	seen := map[string]bool{}
	for i, message := range messages {
		if message == nil {
			continue
		}
		for j, block := range message.ContentBlocks {
			result, ok := observationText(block)
			if !ok {
				continue
			}
			output, found := receipts[result.CallID]
			if !found {
				continue
			}
			if len(message.ContentBlocks) != 1 {
				return nil, nil
			}
			// Do not rewrite another historical occurrence of the same call ID,
			// or accept a changed current observation as a formatting difference.
			value := result.Content[0].Text.Text
			known := value == string(output.raw)
			for _, attempt := range attempts {
				known = known || !attempt.ambiguous && attempt.values[result.CallID] == value
			}
			if seen[result.CallID] || result.Name != output.name || !known {
				return nil, nil
			}
			seen[result.CallID] = true
			positions = append(positions, replayToolPosition{i, j, result.CallID})
		}
	}
	if len(positions) == 0 {
		return nil, nil
	}

	var candidates [][]*schema.AgenticMessage
	variants := map[string]bool{}
	for _, attempt := range attempts {
		if attempt.ambiguous {
			continue
		}
		complete := true
		unchanged := true
		var values []string
		for _, position := range positions {
			value, found := attempt.values[position.callID]
			complete = complete && found
			values = append(values, value)
			current := messages[position.message].ContentBlocks[position.block].FunctionToolResult.Content[0].Text.Text
			unchanged = unchanged && value == current
		}
		key, _ := json.Marshal(values)
		if !complete || unchanged || variants[string(key)] {
			continue
		}
		variants[string(key)] = true
		candidate := append([]*schema.AgenticMessage{}, messages...)
		for _, position := range positions {
			// Copy only the path to the text. Every native block, extension,
			// metadata value and ordering outside this text remains untouched.
			message := *candidate[position.message]
			message.ContentBlocks = append([]*schema.ContentBlock{}, message.ContentBlocks...)
			block := *message.ContentBlocks[position.block]
			result := *block.FunctionToolResult
			result.Content = append([]*schema.FunctionToolResultContentBlock{}, result.Content...)
			part := *result.Content[0]
			text := *part.Text
			text.Text = attempt.values[position.callID]
			part.Text = &text
			result.Content[0] = &part
			block.FunctionToolResult = &result
			message.ContentBlocks[position.block] = &block
			candidate[position.message] = &message
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func (s *Store) observedToolAttempts(ctx context.Context, run *Run, receipts map[string]replayToolOutput) ([]*observedToolAttempt, error) {
	rows, err := s.DB.Query(ctx, `SELECT attempt_id::text,payload FROM runtime_session_events WHERE run_id=$1 AND attempt_id<>$2 AND kind='message' ORDER BY seq`, run.ID, run.Attempt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := map[string]*observedToolAttempt{}
	var order []*observedToolAttempt
	for rows.Next() {
		var attemptID string
		var payload []byte
		if err = rows.Scan(&attemptID, &payload); err != nil {
			return nil, err
		}
		var event adk.SessionEvent[*schema.AgenticMessage]
		if err = decodeSessionPayload(payload, &event); err != nil {
			return nil, err
		}
		if event.Message == nil || event.Message.Role != schema.AgenticRoleTypeUser || len(event.Message.ContentBlocks) != 1 {
			continue
		}
		for _, block := range event.Message.ContentBlocks {
			result, ok := observationText(block)
			if !ok {
				continue
			}
			output, found := receipts[result.CallID]
			if !found {
				continue
			}
			value := result.Content[0].Text.Text
			if result.Name != output.name || !sameObservedReceiptValue(output.raw, value) {
				continue
			}
			attempt := attempts[attemptID]
			if attempt == nil {
				attempt = &observedToolAttempt{values: map[string]string{}}
				attempts[attemptID] = attempt
				order = append(order, attempt)
			}
			if previous, found := attempt.values[result.CallID]; found && previous != value {
				attempt.ambiguous = true
			}
			attempt.values[result.CallID] = value
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return order, nil
}

func observationText(block *schema.ContentBlock) (*schema.FunctionToolResult, bool) {
	if block == nil || block.Type != schema.ContentBlockTypeFunctionToolResult || block.FunctionToolResult == nil {
		return nil, false
	}
	result := block.FunctionToolResult
	if len(result.Content) != 1 || result.Content[0] == nil || result.Content[0].Type != schema.FunctionToolResultContentBlockTypeText || result.Content[0].Text == nil {
		return nil, false
	}
	return result, true
}

// Validation of the observed text is separate from request identity: no
// normalized JSON is ever used as a model hash. Old live DTO output retains
// receipt values; the old cached any decoder may have converted integer Data
// to float64. Both must be actual persisted Session outputs before use.
func sameObservedReceiptValue(receipt []byte, observed string) bool {
	value, err := canonicalObservationValue([]byte(observed))
	if err != nil {
		return false
	}
	original, err := canonicalObservationValue(receipt)
	if err == nil && bytes.Equal(value, original) {
		return true
	}
	var oldCached Observation
	if json.Unmarshal(receipt, &oldCached) != nil {
		return false
	}
	roundTrip, err := json.Marshal(&oldCached)
	if err != nil {
		return false
	}
	roundTrip, err = canonicalObservationValue(roundTrip)
	return err == nil && bytes.Equal(value, roundTrip)
}

func canonicalObservationValue(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, ErrReplayConflict
		}
		return nil, err
	}
	return json.Marshal(value)
}
