package pg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"teamie/services/agent-runtime/internal/domain"
)

// AcceptanceEvidence is an operator-only projection, never a browser DTO.
// All bodies, native output, arguments, provider data and commands stay private.
type AcceptanceEvidence struct {
	Version   int                `json:"version"`
	AgentID   string             `json:"agentId"`
	MessageID string             `json:"messageId"`
	Runs      []EvidenceRun      `json:"runs"`
	Tools     []EvidenceTool     `json:"tools"`
	Schedules []EvidenceSchedule `json:"schedules"`
}
type EvidenceInput struct {
	Kind        string `json:"kind,omitempty"`
	MessageID   string `json:"messageId,omitempty"`
	ScheduleID  string `json:"scheduleId,omitempty"`
	ParentRunID string `json:"parentRunId,omitempty"`
	State       string `json:"state"`
	CreatedAt   string `json:"createdAt,omitempty"`
}
type EvidenceRun struct {
	ID     string          `json:"id"`
	State  string          `json:"state"`
	Inputs []EvidenceInput `json:"inputs"`
}
type EvidenceTool struct {
	RunID          string `json:"runId"`
	CallID         string `json:"callId"`
	Name           string `json:"name"`
	Completed      bool   `json:"completed"`
	OK             bool   `json:"ok"`
	Denied         bool   `json:"denied,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	AttachmentID   string `json:"attachmentId,omitempty"`
	ScheduleID     string `json:"scheduleId,omitempty"`
	TaskID         string `json:"taskId,omitempty"`
}
type EvidenceSchedule struct {
	ID        string     `json:"id"`
	RunID     string     `json:"runId"`
	RunCount  int        `json:"runCount"`
	LastRunAt *time.Time `json:"lastRunAt,omitempty"`
}

// ReadAcceptanceEvidence follows only runs caused by this exact message/Agent.
// The command opens a read-only pool; this method never migrates or changes state.
func (r *Repository) ReadAcceptanceEvidence(ctx context.Context, agentID, messageID string) (AcceptanceEvidence, error) {
	out := AcceptanceEvidence{Version: 1, AgentID: agentID, MessageID: messageID, Runs: []EvidenceRun{}, Tools: []EvidenceTool{}, Schedules: []EvidenceSchedule{}}
	rows, err := r.db.Conn(ctx).Query(ctx, `WITH RECURSIVE causal(id) AS (
 SELECT run_id FROM runtime_inputs WHERE agent_id=$1 AND payload->>'messageId'=$2 AND run_id IS NOT NULL
 UNION
 SELECT i.run_id FROM runtime_inputs i JOIN causal p ON i.payload->>'parentRunId'=p.id::text
 WHERE i.agent_id=$1 AND i.run_id IS NOT NULL
 ) SELECT r.id::text,r.state FROM runtime_runs r JOIN causal c ON c.id=r.id WHERE r.agent_id=$1 ORDER BY r.started_at,r.id LIMIT 257`, agentID, messageID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var run EvidenceRun
		if err = rows.Scan(&run.ID, &run.State); err != nil {
			rows.Close()
			return out, err
		}
		run.Inputs = []EvidenceInput{}
		out.Runs = append(out.Runs, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Runs) > 256 {
		return out, errors.New("acceptance evidence scope too large")
	}
	for index := range out.Runs {
		run := &out.Runs[index]
		inputs, e := r.db.Conn(ctx).Query(ctx, `SELECT COALESCE(payload->>'kind',''),COALESCE(payload->>'messageId',''),COALESCE(payload->>'scheduleId',''),COALESCE(payload->>'parentRunId',''),state,COALESCE(payload->>'createdAt','') FROM runtime_inputs WHERE agent_id=$1 AND run_id=$2 ORDER BY delivery_seq`, agentID, run.ID)
		if e != nil {
			return out, e
		}
		for inputs.Next() {
			var in EvidenceInput
			if e = inputs.Scan(&in.Kind, &in.MessageID, &in.ScheduleID, &in.ParentRunID, &in.State, &in.CreatedAt); e != nil {
				inputs.Close()
				return out, e
			}
			run.Inputs = append(run.Inputs, in)
		}
		e = inputs.Err()
		inputs.Close()
		if e != nil {
			return out, e
		}
		calls, e := r.db.Conn(ctx).Query(ctx, `SELECT t.call_id,t.name,t.request_hash,t.result,o.response FROM runtime_tool_calls t LEFT JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key='context-im-arguments-v1' WHERE t.run_id=$1 ORDER BY t.created_at,t.call_id`, run.ID)
		if e != nil {
			return out, e
		}
		for calls.Next() {
			var call EvidenceTool
			var hash string
			var result, args []byte
			call.RunID = run.ID
			if e = calls.Scan(&call.CallID, &call.Name, &hash, &result, &args); e != nil {
				calls.Close()
				return out, e
			}
			projectEvidenceTool(&call, agentID, hash, result, args)
			out.Tools = append(out.Tools, call)
		}
		e = calls.Err()
		calls.Close()
		if e != nil {
			return out, e
		}
		schedules, e := r.db.Conn(ctx).Query(ctx, `SELECT id,created_run_id::text,run_count,last_run_at FROM runtime_schedules WHERE agent_id=$1 AND created_run_id=$2 ORDER BY id`, agentID, run.ID)
		if e != nil {
			return out, e
		}
		for schedules.Next() {
			var s EvidenceSchedule
			if e = schedules.Scan(&s.ID, &s.RunID, &s.RunCount, &s.LastRunAt); e != nil {
				schedules.Close()
				return out, e
			}
			out.Schedules = append(out.Schedules, s)
		}
		e = schedules.Err()
		schedules.Close()
		if e != nil {
			return out, e
		}
	}
	return out, nil
}

func projectEvidenceTool(call *EvidenceTool, agentID, hash string, result, args []byte) {
	call.Completed = len(result) > 0
	if call.Name == "native:workspace_execute" {
		call.TaskID = "shell-" + domain.Digest([]byte(agentID + "/" + call.RunID + "/" + call.CallID))[:40]
		return // A launch receipt is not proof of a successful completed command.
	}
	if strings.HasPrefix(call.Name, "native:") {
		return
	}
	var obs struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Data  struct {
			ID             string `json:"id"`
			ConversationID string `json:"conversationId"`
			Attachment     struct {
				ID string `json:"id"`
			} `json:"attachment"`
			Schedule struct {
				ID string `json:"scheduleId"`
			} `json:"schedule"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &obs) != nil {
		return
	}
	call.OK = obs.OK
	call.Denied = !obs.OK && (obs.Error == "forbidden" || obs.Error == "not_found")
	switch call.Name {
	case "im_send_message", "im_retract_message":
		call.MessageID = obs.Data.ID
		call.ConversationID = obs.Data.ConversationID
	case "im_open_direct_conversation", "im_create_group_conversation", "im_add_conversation_members":
		call.ConversationID = obs.Data.ID
	case "im_upload_file":
		call.AttachmentID = obs.Data.Attachment.ID
		call.ConversationID = obs.Data.ConversationID
	case "schedule_create":
		call.ScheduleID = obs.Data.Schedule.ID
	}
	var saved struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(args, &saved) == nil && saved.Name == call.Name && domain.Digest(saved.Arguments) == hash {
		var scope struct {
			ConversationID string `json:"conversationId"`
		}
		if json.Unmarshal(saved.Arguments, &scope) == nil {
			call.ConversationID = scope.ConversationID
		}
	}
}
