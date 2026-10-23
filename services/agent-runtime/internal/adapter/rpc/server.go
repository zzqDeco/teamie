// Package rpc implements the inbound Kitex AgentRuntimeService RPC adapter.
package rpc

import (
	"context"
	"encoding/json"
	"time"

	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/agent-runtime/internal/app"
	"teamie/services/agent-runtime/internal/domain"
)

// Server adapts the Agent Runtime application service to the generated
// Kitex AgentRuntimeService interface.
type Server struct {
	App *app.Service
}

var _ wire.AgentRuntimeService = (*Server)(nil)

func toQuery(q *wire.RuntimeRequest) domain.RuntimeQuery {
	if q == nil {
		return domain.RuntimeQuery{}
	}
	return domain.RuntimeQuery{
		ServiceToken: q.ServiceToken,
		AgentID:      q.AgentId,
		OwnerID:      q.OwnerId,
		ResourceID:   q.GetResourceId(),
		Cursor:       q.GetCursor(),
		Limit:        q.GetLimit(),
	}
}

func fromResult(res domain.RuntimeResult, err error) (*wire.RuntimeResponse, error) {
	if err != nil {
		return nil, err
	}
	if res.OK {
		data := res.DataJSON
		return &wire.RuntimeResponse{Ok: true, DataJson: &data}, nil
	}
	status := res.StatusCode
	code := res.ErrorCode
	msg := res.ErrorMessage
	if msg == "" {
		msg = code
	}
	return &wire.RuntimeResponse{StatusCode: &status, ErrorCode: &code, ErrorMessage: &msg}, nil
}

// RuntimeOK encodes v as JSON in a successful wire.RuntimeResponse.
func RuntimeOK(v any) (*wire.RuntimeResponse, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return RuntimeFail(503, "runtime_unavailable")
	}
	s := string(b)
	return &wire.RuntimeResponse{Ok: true, DataJson: &s}, nil
}

// RuntimeFail constructs a structured failure wire.RuntimeResponse.
func RuntimeFail(status int32, code string) (*wire.RuntimeResponse, error) {
	message := code
	return &wire.RuntimeResponse{StatusCode: &status, ErrorCode: &code, ErrorMessage: &message}, nil
}

// RequestLimit normalizes the pagination limit on a wire.RuntimeRequest (default 50, max 100).
func RequestLimit(q *wire.RuntimeRequest) int {
	n := int(q.GetLimit())
	if n <= 0 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}

// AuthorizeRequest validates the service token, UUIDs, and Agent ownership/enabled state via Control.
func AuthorizeRequest(ctx context.Context, control domain.ControlGateway, controlToken string, q *wire.RuntimeRequest, write bool) (*domain.AgentProfile, *wire.RuntimeResponse) {
	if q == nil || !domain.ValidServiceKey(controlToken, q.ServiceToken) {
		v, _ := RuntimeFail(403, "forbidden")
		return nil, v
	}
	if !domain.ValidUUID(q.AgentId) || !domain.ValidUUID(q.OwnerId) {
		v, _ := RuntimeFail(404, "not_found")
		return nil, v
	}
	if control == nil {
		v, _ := RuntimeFail(503, "control_unavailable")
		return nil, v
	}
	pc, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	profile, err := control.GetAgentProfile(pc, q.AgentId)
	if err != nil {
		v, _ := RuntimeFail(503, "control_unavailable")
		return nil, v
	}
	if profile == nil || profile.OwnerID != q.OwnerId {
		v, _ := RuntimeFail(404, "not_found")
		return nil, v
	}
	if write && (!profile.Enabled || profile.Deleted) {
		v, _ := RuntimeFail(409, "agent_disabled")
		return nil, v
	}
	return profile, nil
}

func (s *Server) State(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.State(ctx, toQuery(q))
	return fromResult(res, err)
}

func (s *Server) Runs(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.Runs(ctx, toQuery(q))
	return fromResult(res, err)
}

func (s *Server) Run(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.RunDetail(ctx, toQuery(q))
	return fromResult(res, err)
}

func (s *Server) Tasks(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.Tasks(ctx, toQuery(q))
	return fromResult(res, err)
}

func (s *Server) Task(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.TaskDetail(ctx, toQuery(q))
	return fromResult(res, err)
}

func (s *Server) Events(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.Events(ctx, toQuery(q))
	return fromResult(res, err)
}

func (s *Server) CancelRun(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.CancelRun(ctx, toQuery(q))
	return fromResult(res, err)
}

func (s *Server) RetryRun(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.RetryRun(ctx, toQuery(q))
	return fromResult(res, err)
}

func (s *Server) CancelTask(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	res, err := s.App.CancelTask(ctx, toQuery(q))
	return fromResult(res, err)
}
