package eino

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/agentcontrolservice"
)

type rpcOwnerControl struct {
	agentcontrolservice.Client
	definition *wire.AgentDefinition
}

func (c *rpcOwnerControl) GetAgent(context.Context, *wire.AgentRequest, ...callopt.Option) (*wire.AgentResponse, error) {
	return &wire.AgentResponse{Agent: c.definition}, nil
}
func TestRuntimeOwnerMetadataPrivacyAndDisabledHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent, owner := uuid.NewString(), uuid.NewString()
	lease, err := s.Acquire(ctx, agent, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(ctx, lease)
	inputs := []Input{{EventID: "1", Body: "PRIVATE_MESSAGE_PAYLOAD", SenderID: uuid.NewString(), ConversationID: uuid.NewString()}}
	if err = s.Ingest(ctx, lease, "1", inputs); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(ctx, lease, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, lease, run, "failed", "PRIVATE_RAW_PROVIDER_ERROR", false); err != nil {
		t.Fatal(err)
	}
	store := &TaskStore{Store: s, AgentID: agent}
	task, err := store.Create(WithRun(ctx, run), &bg.CreateTaskRequest{Spec: bg.Spec{ID: "private-task", ExecutorKey: "fixture", Description: "PRIVATE_COMMAND_DESCRIPTION", Payload: []byte("PRIVATE_TOOL_ARGUMENTS")}, LeaseExpiryPolicy: bg.LeaseExpiryRetry})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Start(ctx, &bg.StartTaskRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Complete(ctx, &bg.CompleteTaskRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version, Data: []byte(`{"stdout":"PRIVATE_STDOUT","exitCode":7}`)}); err != nil {
		t.Fatal(err)
	}
	control := &rpcOwnerControl{definition: &wire.AgentDefinition{Id: agent, OwnerId: owner, Enabled: true, Revision: 1}}
	e := &Engine{Store: s, Config: Config{ControlToken: "service"}, Control: control, Sandbox: &asyncSandboxFixture{}}
	rpc := &RuntimeRPC{Engine: e}
	request := &wire.RuntimeRequest{ServiceToken: "service", AgentId: agent, OwnerId: owner}
	runID, taskID := run.ID, task.Spec.ID
	methods := []func(context.Context, *wire.RuntimeRequest) (*wire.RuntimeResponse, error){rpc.State, rpc.Runs, rpc.Tasks, rpc.Events}
	for _, method := range methods {
		response, err := method(ctx, request)
		if err != nil || !response.Ok {
			t.Fatal("owner read rejected", response, err)
		}
		for _, forbidden := range []string{"PRIVATE_", "attempt", "fence", "input_json", `"command":`, `"stdout":`, `"payload":`} {
			if strings.Contains(response.GetDataJson(), forbidden) {
				t.Fatalf("owner metadata leaked %s: %s", forbidden, response.GetDataJson())
			}
		}
	}
	request.ResourceId = &runID
	response, _ := rpc.Run(ctx, request)
	if !response.Ok || strings.Contains(response.GetDataJson(), "PRIVATE_") {
		t.Fatal("run detail leaked input or error")
	}
	request.ResourceId = &taskID
	response, _ = rpc.Task(ctx, request)
	if !response.Ok || !strings.Contains(response.GetDataJson(), "Workspace command") || !strings.Contains(response.GetDataJson(), `"exitCode":7`) {
		t.Fatal("task detail is not generic safe metadata", response.GetDataJson())
	}
	request.OwnerId = uuid.NewString()
	response, _ = rpc.State(ctx, request)
	if response.GetStatusCode() != 404 {
		t.Fatal("ownership/admin isolation bypass")
	}
	request.OwnerId = owner
	request.ServiceToken = "wrong"
	response, _ = rpc.State(ctx, request)
	if response.GetStatusCode() != 403 {
		t.Fatal("service token bypass")
	}
	request.ServiceToken = "service"
	control.definition.Enabled = false
	response, _ = rpc.Tasks(ctx, request)
	if !response.Ok {
		t.Fatal("disable hid persisted task history")
	}
	request.ResourceId = &runID
	response, _ = rpc.RetryRun(ctx, request)
	if response.GetStatusCode() != 409 {
		t.Fatal("disabled agent accepted work")
	}
	one := int32(1)
	request.Limit = &one
	request.ResourceId = nil
	response, _ = rpc.Events(ctx, request)
	var first struct {
		Events  []publicEvent `json:"events"`
		Cursor  string        `json:"cursor"`
		HasMore bool          `json:"hasMore"`
	}
	if json.Unmarshal([]byte(response.GetDataJson()), &first) != nil || len(first.Events) != 1 || first.Cursor == "" || !first.HasMore {
		t.Fatal("event cursor first page invalid")
	}
	request.Cursor = &first.Cursor
	response, _ = rpc.Events(ctx, request)
	var second struct {
		Events []publicEvent `json:"events"`
		Cursor string        `json:"cursor"`
	}
	_ = json.Unmarshal([]byte(response.GetDataJson()), &second)
	if len(second.Events) != 1 || second.Events[0].ID == first.Events[0].ID {
		t.Fatal("event cursor did not advance")
	}
}
func TestNativeInputAndCursorMigrationPreservesV1(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := uuid.NewString()
	l, err := s.Acquire(ctx, agent, "migrate")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(ctx, l)
	if err = s.Ingest(ctx, l, "99", []Input{{EventID: "99", Body: "V1_DURABLE_PAYLOAD"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, schemaSQL+schemaV2SQL); err != nil {
		t.Fatal(err)
	}
	var cursor, body string
	if err = s.DB.QueryRow(ctx, `SELECT a.cursor::text,i.payload->>'body' FROM runtime_agents a JOIN runtime_inputs i ON i.agent_id=a.agent_id WHERE a.agent_id=$1`, agent).Scan(&cursor, &body); err != nil || cursor != "99" || body != "V1_DURABLE_PAYLOAD" {
		t.Fatal("forward migration changed existing inbox/cursor", err)
	}
}
func (c *rpcOwnerControl) ReportRuntime(context.Context, *wire.RuntimeReport, ...callopt.Option) (*wire.Ack, error) {
	return &wire.Ack{Ok: true}, nil
}
func (c *rpcOwnerControl) ListAgents(context.Context, *wire.AgentListRequest, ...callopt.Option) (*wire.AgentListResponse, error) {
	return &wire.AgentListResponse{Agents: []*wire.AgentDefinition{c.definition}}, nil
}
func (c *rpcOwnerControl) Ping(context.Context, *wire.PingRequest, ...callopt.Option) (*wire.PingResponse, error) {
	return &wire.PingResponse{Ready: true}, nil
}
