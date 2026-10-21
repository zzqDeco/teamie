package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/modeltransport"
)

func (s *Service) decodeHTTPCall(w http.ResponseWriter, r *http.Request) (*modeltransport.CallRequest, bool) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || !api.EqualSecret(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.Token) {
		api.Fail(w, api.Err(401, "unauthorized", "Invalid service credential"))
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes*2+4096)
	var in modeltransport.CallRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || !validID(in.OwnerID) || !validID(in.AgentID) || !validID(in.SessionID) || !validID(in.RunID) || in.MessageLimit < 0 || in.MessageLimit > modeltransport.MaxInputMessages {
		api.Fail(w, api.Err(400, "invalid_request", "Invalid model request"))
		return nil, false
	}
	return &in, true
}

// Hertz's disconnect sensing cancels r.Context even before response headers
// exist. Ordinary Thrift unary handlers cannot observe that lifecycle.
func (s *Service) generateHTTP(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeHTTPCall(w, r)
	if !ok {
		return
	}
	ctx := withExpectedModelConfigID(r.Context(), r.Header.Get(modeltransport.ModelConfigIDHeader))
	out := s.call(ctx, in.OwnerID, in.AgentID, in.SessionID, in.RunID, in.RequestJSON, in.InputMessageLimit())
	if out.GetModelConfigId() != "" {
		w.Header().Set(modeltransport.ModelConfigIDHeader, out.GetModelConfigId())
		out.ModelConfigId = nil
	}
	api.Write(w, 200, out)
}
