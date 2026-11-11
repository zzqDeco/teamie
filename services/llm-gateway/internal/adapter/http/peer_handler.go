package httpAdapter

import (
	"net/http"
	"strings"

	"go.uber.org/zap"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/services/llm-gateway/internal/domain"
)

const (
	peerCancelPath    = "/internal/v1/peer/cancel"
	peerReconcilePath = "/internal/v1/peer/reconcile"
)

type peerCancelReq struct {
	AgentID string `json:"agentId"`
	RunID   string `json:"runId"`
}

type peerReconcileReq struct {
	ProviderID string `json:"providerId"`
}

func (a *Adapter) handlePeerCancel(w http.ResponseWriter, r *http.Request) {
	if err := a.App.AuthorizeBearer(r.Header.Get("Authorization")); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := httpx.RequireJSONMediaType(r); err != nil {
		httpx.Fail(w, err)
		return
	}
	localID := a.App.InstanceID()
	w.Header().Set(domain.PeerInstanceHeader, localID)
	if strings.TrimSpace(r.Header.Get(domain.PeerInstanceHeader)) == localID {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var req peerCancelReq
	if err := httpx.DecodeJSONMaxBytes(w, r, 4096, &req); err != nil {
		httpx.Fail(w, err)
		return
	}
	if !domain.ValidID(req.AgentID) || !domain.ValidID(req.RunID) {
		httpx.Fail(w, domain.Err(400, "invalid_request", "Invalid attribution identifiers"))
		return
	}
	observability.AnnotateHTTP(w,
		zap.String("agent.id", req.AgentID),
		zap.String("agent.run.id", req.RunID),
	)
	a.App.CancelRunLocal(req.AgentID, req.RunID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Adapter) handlePeerReconcile(w http.ResponseWriter, r *http.Request) {
	if err := a.App.AuthorizeBearer(r.Header.Get("Authorization")); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := httpx.RequireJSONMediaType(r); err != nil {
		httpx.Fail(w, err)
		return
	}
	localID := a.App.InstanceID()
	w.Header().Set(domain.PeerInstanceHeader, localID)
	if strings.TrimSpace(r.Header.Get(domain.PeerInstanceHeader)) == localID {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var req peerReconcileReq
	if err := httpx.DecodeJSONMaxBytes(w, r, 4096, &req); err != nil {
		httpx.Fail(w, err)
		return
	}
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	if req.ProviderID != "" && !domain.ValidProviderID(req.ProviderID) {
		httpx.Fail(w, domain.Err(400, "invalid_request", "Invalid model provider ID"))
		return
	}
	if err := a.App.ReconcileConfig(r.Context(), req.ProviderID); err != nil {
		httpx.Fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
