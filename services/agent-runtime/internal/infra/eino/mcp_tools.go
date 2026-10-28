package eino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
	"teamie/pkg/observability"
	"teamie/services/agent-runtime/internal/domain"
)

const (
	mcpToolsObservationCallID = "__mcp_tools__"
	mcpToolsObservationKind   = "mcp_tools_snapshot/v1"
)

const marketplaceAppGuidance = `

Marketplace Group Apps (MCP Tools & TEAMIE_APP_NOTIFICATION):
- In group conversations with installed Marketplace apps, external MCP tools are exposed as mcp__<slug>__<tool>.
- Each MCP tool is pre-bound to its group conversation and authenticates with a short-lived symmetric Context Token (carrying your userId, displayName, workspaceId, conversationId, and appId).
- When an installed Marketplace app's MCP server pushes a notification, Runtime delivers a TEAMIE_APP_NOTIFICATION input envelope with body containing appId, slug, appName, method, and params.
- Upon receiving TEAMIE_APP_NOTIFICATION or a group message about an app, inspect the state or invoke the appropriate mcp__<slug>__<tool> tools, and call im_send_message only when you want to speak in the group conversation.`

type imMarketplaceApp struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	WebURL      string `json:"webUrl"`
	MCPURL      string `json:"mcpUrl"`
	Enabled     bool   `json:"enabled"`
}

type imConversationAppSession struct {
	App       imMarketplaceApp `json:"app"`
	Token     string           `json:"token"`
	ExpiresAt string           `json:"expiresAt"`
}

// AppNotificationBody is the JSON payload stored in Input.Body for TEAMIE_APP_NOTIFICATION inputs.
type AppNotificationBody struct {
	AppID   string          `json:"appId"`
	Slug    string          `json:"slug"`
	AppName string          `json:"appName"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpPinnedToolSpec struct {
	AppID          string           `json:"appId"`
	AppSlug        string           `json:"appSlug"`
	AppName        string           `json:"appName"`
	MCPURL         string           `json:"mcpUrl"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId"`
	ExposedName    string           `json:"exposedName"`
	RemoteName     string           `json:"remoteName"`
	ToolInfo       *schema.ToolInfo `json:"toolInfo"`
}

// BuildMCPTools discovers or restores the Run-pinned Marketplace MCP tools for any group
// conversations present in the Run's inputs using github.com/cloudwego/eino-ext/components/tool/mcp.
func (t *Tools) BuildMCPTools(ctx context.Context, run *Run) ([]tool.BaseTool, error) {
	if t == nil || t.Store == nil || t.Store.DB == nil || run == nil {
		return nil, nil
	}
	rawSnap, found, err := t.Store.DB.ReadObservation(ctx, run.ID, mcpToolsObservationCallID, mcpToolsObservationKind)
	if err != nil {
		return nil, err
	}
	if found {
		var specs []mcpPinnedToolSpec
		if err := json.Unmarshal(rawSnap, &specs); err != nil {
			return nil, ErrReplayConflict
		}
		out := make([]tool.BaseTool, 0, len(specs))
		for _, spec := range specs {
			if spec.ToolInfo == nil || spec.ExposedName == "" {
				return nil, ErrReplayConflict
			}
			out = append(out, &mcpToolWrapper{tools: t, spec: spec})
		}
		return out, nil
	}

	// Historical or already-started Runs without a pinned MCP snapshot must not
	// acquire new tools on replay.
	existingModels, err := t.Store.DB.ListModelCallResponses(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	if len(existingModels) > 0 {
		return nil, nil
	}

	if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "im.marketplace.enabled") {
		return nil, nil
	}
	if t.IM == nil || strings.TrimSpace(t.IM.BaseURL) == "" {
		return nil, nil
	}

	convWorkspaces := map[string]string{}
	convIDs := []string{}
	for _, in := range run.Inputs {
		if domain.ValidUUID(in.ConversationID) {
			if _, exists := convWorkspaces[in.ConversationID]; !exists {
				convWorkspaces[in.ConversationID] = in.WorkspaceID
				convIDs = append(convIDs, in.ConversationID)
			} else if convWorkspaces[in.ConversationID] == "" && in.WorkspaceID != "" {
				convWorkspaces[in.ConversationID] = in.WorkspaceID
			}
		}
	}
	if len(convIDs) == 0 {
		return nil, nil
	}
	sort.Strings(convIDs)

	specs := []mcpPinnedToolSpec{}
	usedNames := map[string]struct{}{}
	for _, convID := range convIDs {
		wsID := convWorkspaces[convID]
		var appsOut struct {
			Apps []imMarketplaceApp `json:"apps"`
		}
		if err := t.IM.Call(ctx, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(convID)+"/apps", nil, &appsOut); err != nil {
			var api *APIError
			if errors.As(err, &api) && (api.Status == 400 || api.Status == 403 || api.Status == 404) {
				continue
			}
			return nil, err
		}
		if len(appsOut.Apps) == 0 {
			continue
		}
		sort.Slice(appsOut.Apps, func(i, j int) bool {
			if appsOut.Apps[i].Slug != appsOut.Apps[j].Slug {
				return appsOut.Apps[i].Slug < appsOut.Apps[j].Slug
			}
			return appsOut.Apps[i].ID < appsOut.Apps[j].ID
		})
		for _, app := range appsOut.Apps {
			if !app.Enabled || strings.TrimSpace(app.MCPURL) == "" {
				continue
			}
			var sessOut imConversationAppSession
			if err := t.IM.Call(ctx, http.MethodPost, "/api/v1/conversations/"+url.PathEscape(convID)+"/apps/"+url.PathEscape(app.ID)+"/session", map[string]any{}, &sessOut); err != nil {
				var api *APIError
				if errors.As(err, &api) && (api.Status == 400 || api.Status == 403 || api.Status == 404) {
					continue
				}
				return nil, err
			}
			if sessOut.App.MCPURL == "" {
				sessOut.App = app
			}
			discovered, discoverErr := discoverMCPAppTools(ctx, sessOut)
			if discoverErr != nil {
				observability.L(ctx).Warn(
					"MCP app tool discovery failed",
					zap.String("event.action", "mcp-tool-discovery-failed"),
					zap.String("custom.app.id", observability.ID(app.ID)),
				)
				continue
			}
			for _, item := range discovered {
				exposedName := formatMCPToolName(app.Slug, item.Name, usedNames, convID)
				pinnedInfo := &schema.ToolInfo{
					Name:        exposedName,
					Desc:        fmt.Sprintf("[Marketplace App %q (slug=%s, appId=%s, workspaceId=%s, conversationId=%s)] %s", app.Name, app.Slug, app.ID, wsID, convID, strings.TrimSpace(item.Desc)),
					Extra:       item.Extra,
					ParamsOneOf: item.ParamsOneOf,
				}
				specs = append(specs, mcpPinnedToolSpec{
					AppID:          app.ID,
					AppSlug:        app.Slug,
					AppName:        app.Name,
					MCPURL:         sessOut.App.MCPURL,
					WorkspaceID:    wsID,
					ConversationID: convID,
					ExposedName:    exposedName,
					RemoteName:     item.Name,
					ToolInfo:       pinnedInfo,
				})
			}
		}
	}
	if len(specs) == 0 {
		return nil, nil
	}
	snapBytes, err := json.Marshal(specs)
	if err != nil {
		return nil, err
	}
	if err := t.Store.DB.SaveObservation(ctx, t.Lease, run.ID, mcpToolsObservationCallID, mcpToolsObservationKind, snapBytes); err != nil {
		return nil, err
	}
	out := make([]tool.BaseTool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, &mcpToolWrapper{tools: t, spec: spec})
	}
	return out, nil
}

func discoverMCPAppTools(ctx context.Context, sess imConversationAppSession) ([]*schema.ToolInfo, error) {
	discoverCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	headers := map[string]string{
		"Authorization": "Bearer " + sess.Token,
	}
	cli, err := client.NewStreamableHttpClient(sess.App.MCPURL, transport.WithHTTPHeaders(headers))
	if err != nil {
		return nil, err
	}
	defer func() { _ = cli.Close() }()

	if err := cli.Start(discoverCtx); err != nil {
		return nil, err
	}
	if _, err := cli.Initialize(discoverCtx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "teamie-agent-runtime",
				Version: "1.0.0",
			},
		},
	}); err != nil {
		return nil, err
	}

	einoTools, err := einomcp.GetTools(discoverCtx, &einomcp.Config{
		Cli:           cli,
		CustomHeaders: headers,
	})
	if err != nil {
		return nil, err
	}

	infos := make([]*schema.ToolInfo, 0, len(einoTools))
	for _, bt := range einoTools {
		info, err := bt.Info(discoverCtx)
		if err != nil {
			return nil, err
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos, nil
}

func sanitizeMCPToolSegment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	res := strings.Trim(b.String(), "_")
	if res == "" {
		return "tool"
	}
	return res
}

func formatMCPToolName(slug, remoteName string, used map[string]struct{}, convID string) string {
	base := "mcp__" + sanitizeMCPToolSegment(slug) + "__" + sanitizeMCPToolSegment(remoteName)
	if _, exists := used[base]; !exists {
		used[base] = struct{}{}
		return base
	}
	shortConv := sanitizeMCPToolSegment(convID)
	if len(shortConv) > 8 {
		shortConv = shortConv[:8]
	}
	candidate := base + "__" + shortConv
	used[candidate] = struct{}{}
	return candidate
}

type mcpToolWrapper struct {
	tools *Tools
	spec  mcpPinnedToolSpec
}

func (w *mcpToolWrapper) Info(_ context.Context) (*schema.ToolInfo, error) {
	return w.spec.ToolInfo, nil
}

func (w *mcpToolWrapper) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	trimmed := strings.TrimSpace(argumentsInJSON)
	if trimmed == "" {
		trimmed = "{}"
	}
	if !json.Valid([]byte(trimmed)) {
		obs, err := w.tools.execute(ctx, w.spec.ExposedName, trimmed, func(context.Context, string) (*Observation, error) {
			return &Observation{Error: "invalid_arguments"}, nil
		})
		if err != nil {
			return "", err
		}
		return marshalObservation(ctx, obs)
	}

	obs, err := w.tools.execute(ctx, w.spec.ExposedName, json.RawMessage(trimmed), func(execCtx context.Context, _ string) (*Observation, error) {
		if w.tools.FlagsFile != "" && !runtimeFlag(w.tools.FlagsFile, "im.marketplace.enabled") {
			return &Observation{Error: "feature_disabled"}, nil
		}
		var sessOut imConversationAppSession
		if err := w.tools.IM.Call(execCtx, http.MethodPost, "/api/v1/conversations/"+url.PathEscape(w.spec.ConversationID)+"/apps/"+url.PathEscape(w.spec.AppID)+"/session", map[string]any{}, &sessOut); err != nil {
			return observeError(err), nil
		}
		callCtx, cancel := context.WithTimeout(execCtx, 15*time.Second)
		defer cancel()

		headers := map[string]string{
			"Authorization": "Bearer " + sessOut.Token,
		}
		mcpURL := sessOut.App.MCPURL
		if mcpURL == "" {
			mcpURL = w.spec.MCPURL
		}
		cli, err := client.NewStreamableHttpClient(mcpURL, transport.WithHTTPHeaders(headers))
		if err != nil {
			return &Observation{Error: "mcp_unavailable"}, nil
		}
		defer func() { _ = cli.Close() }()

		if err := cli.Start(callCtx); err != nil {
			return &Observation{Error: "mcp_unavailable"}, nil
		}
		if _, err := cli.Initialize(callCtx, mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				ClientInfo: mcp.Implementation{
					Name:    "teamie-agent-runtime",
					Version: "1.0.0",
				},
			},
		}); err != nil {
			return &Observation{Error: "mcp_unavailable"}, nil
		}

		einoTools, err := einomcp.GetTools(callCtx, &einomcp.Config{
			Cli:           cli,
			ToolNameList:  []string{w.spec.RemoteName},
			CustomHeaders: headers,
		})
		if err != nil || len(einoTools) == 0 {
			return &Observation{Error: "mcp_tool_not_found"}, nil
		}
		invokable, ok := einoTools[0].(tool.InvokableTool)
		if !ok {
			return &Observation{Error: "mcp_tool_not_found"}, nil
		}
		rawOut, err := invokable.InvokableRun(callCtx, trimmed, opts...)
		if err != nil {
			return &Observation{
				OK:    false,
				Error: "mcp_tool_error",
				Data: map[string]any{
					"appId":          w.spec.AppID,
					"slug":           w.spec.AppSlug,
					"workspaceId":    w.spec.WorkspaceID,
					"conversationId": w.spec.ConversationID,
					"tool":           w.spec.RemoteName,
				},
			}, nil
		}
		var parsed any
		if err := json.Unmarshal([]byte(rawOut), &parsed); err != nil {
			parsed = rawOut
		}
		return &Observation{
			OK: true,
			Data: map[string]any{
				"appId":          w.spec.AppID,
				"slug":           w.spec.AppSlug,
				"workspaceId":    w.spec.WorkspaceID,
				"conversationId": w.spec.ConversationID,
				"tool":           w.spec.RemoteName,
				"result":         parsed,
			},
		}, nil
	})
	if err != nil {
		return "", err
	}
	return marshalObservation(ctx, obs)
}

var _ tool.InvokableTool = (*mcpToolWrapper)(nil)

func (e *Engine) authorizeAppNotificationInput(ctx context.Context, imc *IM, _ *Lease, in Input) error {
	if !domain.ValidUUID(in.WorkspaceID) || !domain.ValidUUID(in.ConversationID) {
		return errContextUnavailable
	}
	if e.Config.FlagsFile != "" && !runtimeFlag(e.Config.FlagsFile, "im.marketplace.enabled") {
		return errContextUnavailable
	}
	var body AppNotificationBody
	if err := json.Unmarshal([]byte(in.Body), &body); err != nil || !domain.ValidUUID(body.AppID) {
		return errContextUnavailable
	}
	var out struct {
		Apps []imMarketplaceApp `json:"apps"`
	}
	if err := imc.Call(ctx, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(in.ConversationID)+"/apps", nil, &out); err != nil {
		var api *APIError
		if errors.As(err, &api) && (api.Status == 400 || api.Status == 403 || api.Status == 404) {
			return errContextUnavailable
		}
		return err
	}
	for _, app := range out.Apps {
		if app.ID == body.AppID && app.Enabled {
			return nil
		}
	}
	return errContextUnavailable
}

type mcpWatchKey struct {
	ConversationID string
	AppID          string
}

type mcpActiveSubscription struct {
	key         mcpWatchKey
	workspaceID string
	app         imMarketplaceApp
	expiresAt   time.Time
	cli         *client.Client
	cancel      context.CancelFunc
	lost        atomic.Bool
}

type mcpNotificationWatcher struct {
	store     *Store
	lease     *Lease
	im        *IM
	flagsFile string
	wakeCh    chan struct{}

	mu   sync.Mutex
	subs map[mcpWatchKey]*mcpActiveSubscription
}

func newMCPNotificationWatcher(store *Store, lease *Lease, im *IM, flagsFile string) *mcpNotificationWatcher {
	return &mcpNotificationWatcher{
		store:     store,
		lease:     lease,
		im:        im,
		flagsFile: flagsFile,
		wakeCh:    make(chan struct{}, 1),
		subs:      map[mcpWatchKey]*mcpActiveSubscription{},
	}
}

func (w *mcpNotificationWatcher) wake() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.wakeCh
}

func (w *mcpNotificationWatcher) closeAll() {
	if w == nil {
		return
	}
	w.mu.Lock()
	subs := make([]*mcpActiveSubscription, 0, len(w.subs))
	for k, sub := range w.subs {
		subs = append(subs, sub)
		delete(w.subs, k)
	}
	w.mu.Unlock()
	for _, sub := range subs {
		sub.cancel()
		_ = sub.cli.Close()
	}
}

func (w *mcpNotificationWatcher) sync(ctx context.Context) {
	if w == nil || w.store == nil || w.lease == nil || w.im == nil || strings.TrimSpace(w.im.BaseURL) == "" {
		return
	}
	if w.flagsFile != "" && !runtimeFlag(w.flagsFile, "im.marketplace.enabled") {
		w.closeAll()
		return
	}
	var b Bootstrap
	if err := w.im.Call(ctx, http.MethodGet, "/api/v1/bootstrap", nil, &b); err != nil {
		return
	}
	groupConvs := make([]Conversation, 0)
	for _, c := range b.Conversations {
		if c.Type == "group" && domain.ValidUUID(c.ID) && domain.ValidUUID(c.WorkspaceID) {
			groupConvs = append(groupConvs, c)
		}
	}
	if len(groupConvs) == 0 {
		w.closeAll()
		return
	}

	type desiredTarget struct {
		workspaceID string
		convID      string
		app         imMarketplaceApp
	}
	desired := map[mcpWatchKey]desiredTarget{}
	for _, c := range groupConvs {
		var appsOut struct {
			Apps []imMarketplaceApp `json:"apps"`
		}
		if err := w.im.Call(ctx, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(c.ID)+"/apps", nil, &appsOut); err != nil {
			continue
		}
		for _, app := range appsOut.Apps {
			if app.Enabled && strings.TrimSpace(app.MCPURL) != "" && domain.ValidUUID(app.ID) {
				key := mcpWatchKey{ConversationID: c.ID, AppID: app.ID}
				desired[key] = desiredTarget{
					workspaceID: c.WorkspaceID,
					convID:      c.ID,
					app:         app,
				}
			}
		}
	}

	now := time.Now().UTC()
	var toClose []*mcpActiveSubscription
	w.mu.Lock()
	for key, sub := range w.subs {
		target, ok := desired[key]
		if !ok || sub.lost.Load() || sub.app.MCPURL != target.app.MCPURL || (!sub.expiresAt.IsZero() && now.Add(time.Minute).After(sub.expiresAt)) {
			toClose = append(toClose, sub)
			delete(w.subs, key)
		}
	}
	w.mu.Unlock()
	for _, sub := range toClose {
		sub.cancel()
		_ = sub.cli.Close()
	}

	for key, target := range desired {
		w.mu.Lock()
		_, exists := w.subs[key]
		w.mu.Unlock()
		if exists {
			continue
		}
		w.startSubscription(ctx, key, target.workspaceID, target.convID, target.app)
	}
}

func (w *mcpNotificationWatcher) startSubscription(ctx context.Context, key mcpWatchKey, workspaceID, convID string, app imMarketplaceApp) {
	var sessOut imConversationAppSession
	if err := w.im.Call(ctx, http.MethodPost, "/api/v1/conversations/"+url.PathEscape(convID)+"/apps/"+url.PathEscape(app.ID)+"/session", map[string]any{}, &sessOut); err != nil {
		return
	}
	expiresAt, _ := time.Parse(time.RFC3339, sessOut.ExpiresAt)
	headers := map[string]string{
		"Authorization": "Bearer " + sessOut.Token,
	}
	mcpURL := sessOut.App.MCPURL
	if mcpURL == "" {
		mcpURL = app.MCPURL
	}
	cli, err := client.NewStreamableHttpClient(
		mcpURL,
		transport.WithHTTPHeaders(headers),
		transport.WithContinuousListening(),
	)
	if err != nil {
		return
	}
	subCtx, cancel := context.WithCancel(ctx)
	sub := &mcpActiveSubscription{
		key:         key,
		workspaceID: workspaceID,
		app:         app,
		expiresAt:   expiresAt,
		cli:         cli,
		cancel:      cancel,
	}
	cli.OnConnectionLost(func(error) {
		sub.lost.Store(true)
	})
	cli.OnNotification(func(n mcp.JSONRPCNotification) {
		w.handleNotification(subCtx, workspaceID, convID, app, n)
	})

	if err := cli.Start(subCtx); err != nil {
		cancel()
		_ = cli.Close()
		return
	}
	initCtx, initCancel := context.WithTimeout(subCtx, 10*time.Second)
	_, initErr := cli.Initialize(initCtx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "teamie-agent-runtime",
				Version: "1.0.0",
			},
		},
	})
	initCancel()
	if initErr != nil {
		cancel()
		_ = cli.Close()
		return
	}

	w.mu.Lock()
	if ctx.Err() != nil {
		w.mu.Unlock()
		cancel()
		_ = cli.Close()
		return
	}
	w.subs[key] = sub
	w.mu.Unlock()
}

func (w *mcpNotificationWatcher) handleNotification(ctx context.Context, workspaceID, convID string, app imMarketplaceApp, n mcp.JSONRPCNotification) {
	method := strings.TrimSpace(n.Method)
	if method == "" {
		return
	}
	var paramsRaw json.RawMessage
	if raw, err := json.Marshal(n.Params); err == nil && string(raw) != "{}" && string(raw) != "null" {
		paramsRaw = raw
	}
	bodyBytes, err := json.Marshal(AppNotificationBody{
		AppID:   app.ID,
		Slug:    app.Slug,
		AppName: app.Name,
		Method:  method,
		Params:  paramsRaw,
	})
	if err != nil {
		return
	}
	bodyStr := string(bodyBytes)

	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	inserted := false
	err = w.store.DB.WithinFencedTx(pctx, w.lease, func(txCtx context.Context) error {
		var existing int
		if err := w.store.DB.QueryRow(
			txCtx,
			`SELECT count(*) FROM runtime_inputs WHERE agent_id=$1 AND state IN ('pending','processing') AND payload->>'kind'='app.notification' AND payload->>'conversationId'=$2 AND payload->>'body'=$3`,
			w.lease.AgentID,
			convID,
			bodyStr,
		).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		var seq string
		if err := w.store.DB.QueryRow(txCtx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&seq); err != nil {
			return err
		}
		notifID := "mcp-notif:" + uuid.NewString()
		in := Input{
			EventID:        seq,
			Kind:           "app.notification",
			WorkspaceID:    workspaceID,
			ConversationID: convID,
			NotificationID: notifID,
			Body:           bodyStr,
			CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
			SourceRefs:     []InputSource{{WorkspaceID: workspaceID}},
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		tag, err := w.store.DB.Exec(
			txCtx,
			`INSERT INTO runtime_inputs(agent_id,event_id,payload,notification_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			w.lease.AgentID,
			seq,
			raw,
			notifID,
		)
		if err != nil {
			return err
		}
		inserted = tag.RowsAffected() > 0
		return nil
	})
	if err == nil && inserted {
		observability.L(ctx).Info(
			"Marketplace MCP notification persisted",
			zap.String("event.action", "mcp-notification-persisted"),
			zap.String("custom.agent.id", w.lease.AgentID),
			zap.String("custom.app.id", observability.ID(app.ID)),
		)
		select {
		case w.wakeCh <- struct{}{}:
		default:
		}
	}
}
