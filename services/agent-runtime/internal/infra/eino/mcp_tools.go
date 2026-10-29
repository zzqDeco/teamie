package eino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
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
	mcpToolsObservationCallID     = "__mcp_tools__"
	mcpToolsObservationKind       = "mcp_tools_snapshot/v1"
	presenceInitObservationCallID = "__presence_init__"
	presenceInitObservationKind   = "presence_snapshot/v1"
	defaultUnreadDigestInterval   = time.Minute
)

const marketplaceAppGuidance = `

Marketplace Group Apps (MCP Tools & TEAMIE_APP_NOTIFICATION):
- In group conversations with installed Marketplace apps, external MCP tools are exposed as mcp__<slug>__<tool>.
- Each MCP tool is pre-bound to its group conversation and authenticates with a short-lived symmetric Context Token (carrying your userId, displayName, workspaceId, conversationId, and appId).
- If a tool returns mcp_result_unknown, its action may already have completed. Inspect current app state with a read tool before deciding what remains to do; do not repeat the action or claim success without evidence.
- When an installed Marketplace app's MCP server pushes a notification, Runtime delivers a TEAMIE_APP_NOTIFICATION input envelope with body containing appId, slug, appName, method, and params.
- Upon receiving TEAMIE_APP_NOTIFICATION or a group message about an app, inspect the state or invoke the appropriate mcp__<slug>__<tool> tools, and call im_send_message only when you want to speak in the group conversation.`

const presenceAndMarketplaceGuidance = `

Conversation Presence, Unread Digest & Dynamic Group Marketplace Apps:
- You reside in at most ONE active conversation at a time (activeConversationId, across group, channel, and direct conversations).
- Messages in your current activeConversationId arrive immediately as TEAMIE_NEW_MESSAGE envelopes and advance your IM read cursor.
- Messages in all other conversations (or while activeConversationId is empty) do NOT interrupt immediately and do NOT mark you as read; instead they coalesce into a 1-minute TEAMIE_UNREAD_DIGEST envelope containing activeConversationId and unreadConversations (workspaceId, conversationId, name, type, unreadCount, mentionCount, lastSeq).
- You may stay focused on your current active conversation and defer reading other conversations in TEAMIE_UNREAD_DIGEST until ready. Whenever you want to proactively check global unread counts without waiting for the 1-minute digest, call im_list_conversations.
- To enter or switch to any group, channel, or direct conversation, call im_enter_conversation with conversationId (which loads recent messages, advances your IM read cursor there, and sets activeConversationId). Call im_leave_conversation when you want to leave the active conversation without entering another.
- im_send_message requires conversationId to match your current activeConversationId (otherwise it returns not_in_conversation); call im_enter_conversation first before speaking in a conversation.
- When your activeConversationId is a group conversation with installed Marketplace apps, Runtime dynamically mounts that group's mcp__<slug>__<tool> tools on the very next model step of the same Turn and maintains live TEAMIE_APP_NOTIFICATION delivery for that active group only. Switching to another conversation or calling im_leave_conversation automatically unmounts previous group MCP tools and closes its notification stream.
- Each MCP tool authenticates with a short-lived symmetric Context Token (userId, displayName, workspaceId, conversationId, appId). If an MCP tool returns mcp_result_unknown, its side effect may already have occurred; inspect app state with a read tool before deciding what remains rather than repeating the action.`

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

// UnreadConversationSummary describes unread activity in a non-active conversation.
type UnreadConversationSummary struct {
	WorkspaceID    string `json:"workspaceId"`
	ConversationID string `json:"conversationId"`
	Name           string `json:"name,omitempty"`
	Type           string `json:"type,omitempty"`
	UnreadCount    int    `json:"unreadCount"`
	MentionCount   int    `json:"mentionCount,omitempty"`
	LastSeq        int64  `json:"lastSeq,omitempty"`
}

// UnreadDigestBody is the JSON payload stored in Input.Body for TEAMIE_UNREAD_DIGEST inputs.
type UnreadDigestBody struct {
	ActiveConversationID string                      `json:"activeConversationId"`
	UnreadConversations  []UnreadConversationSummary `json:"unreadConversations"`
}

type presenceInitSnapshot struct {
	WorkspaceID    string `json:"workspaceId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	ConvType       string `json:"convType,omitempty"`
}

type persistedUnreadEntry struct {
	WorkspaceID    string `json:"workspaceId"`
	ConversationID string `json:"conversationId"`
	Name           string `json:"name,omitempty"`
	ConvType       string `json:"convType,omitempty"`
	UnreadCount    int    `json:"unreadCount"`
	MentionCount   int    `json:"mentionCount,omitempty"`
	LastSeq        int64  `json:"lastSeq,omitempty"`
	Dirty          bool   `json:"dirty,omitempty"`
}

type persistedPresenceCheckpoint struct {
	ActiveWorkspaceID    string                 `json:"activeWorkspaceId,omitempty"`
	ActiveConversationID string                 `json:"activeConversationId,omitempty"`
	ActiveConvType       string                 `json:"activeConvType,omitempty"`
	Unread               []persistedUnreadEntry `json:"unread,omitempty"`
	FirstDirtyAt         string                 `json:"firstDirtyAt,omitempty"`
	LastDigestAt         string                 `json:"lastDigestAt,omitempty"`
}

func presenceCheckpointID(agentID string) string {
	return "presence:" + agentID
}

func (p *conversationPresenceState) Restore(ctx context.Context, store *Store, lease *Lease) error {
	if p == nil || store == nil || store.DB == nil || lease == nil || lease.AgentID == "" {
		return nil
	}
	raw, _, ok, err := store.DB.GetCheckpoint(ctx, presenceCheckpointID(lease.AgentID))
	if err != nil || !ok || len(raw) == 0 {
		return err
	}
	var cp persistedPresenceCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil
	}
	p.mu.Lock()
	p.activeWorkspaceID = cp.ActiveWorkspaceID
	p.activeConversationID = cp.ActiveConversationID
	p.activeConvType = cp.ActiveConvType
	p.unread = make(map[string]*unreadConversationEntry, len(cp.Unread))
	for _, item := range cp.Unread {
		if !domain.ValidUUID(item.ConversationID) || item.UnreadCount <= 0 {
			continue
		}
		p.unread[item.ConversationID] = &unreadConversationEntry{
			workspaceID:    item.WorkspaceID,
			conversationID: item.ConversationID,
			name:           item.Name,
			convType:       item.ConvType,
			unreadCount:    item.UnreadCount,
			mentionCount:   item.MentionCount,
			lastSeq:        item.LastSeq,
			dirty:          item.Dirty,
		}
	}
	if cp.FirstDirtyAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, cp.FirstDirtyAt); err == nil {
			p.firstDirtyAt = t
		}
	}
	if cp.LastDigestAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, cp.LastDigestAt); err == nil {
			p.lastDigestAt = t
		}
	}
	p.mu.Unlock()
	return nil
}

func (p *conversationPresenceState) Persist(ctx context.Context, store *Store, lease *Lease) error {
	if p == nil || store == nil || store.DB == nil || lease == nil || lease.AgentID == "" {
		return nil
	}
	// Order snapshot creation through the durable write. The state mutex alone
	// cannot prevent an older snapshot from overwriting a newer checkpoint after
	// ingestion has advanced its IM cursor under the same worker lease.
	p.persistMu.Lock()
	defer p.persistMu.Unlock()
	p.mu.Lock()
	cp := persistedPresenceCheckpoint{
		ActiveWorkspaceID:    p.activeWorkspaceID,
		ActiveConversationID: p.activeConversationID,
		ActiveConvType:       p.activeConvType,
		Unread:               make([]persistedUnreadEntry, 0, len(p.unread)),
	}
	for _, entry := range p.unread {
		if entry.unreadCount <= 0 {
			continue
		}
		cp.Unread = append(cp.Unread, persistedUnreadEntry{
			WorkspaceID:    entry.workspaceID,
			ConversationID: entry.conversationID,
			Name:           entry.name,
			ConvType:       entry.convType,
			UnreadCount:    entry.unreadCount,
			MentionCount:   entry.mentionCount,
			LastSeq:        entry.lastSeq,
			Dirty:          entry.dirty,
		})
	}
	sort.Slice(cp.Unread, func(i, j int) bool {
		return cp.Unread[i].ConversationID < cp.Unread[j].ConversationID
	})
	if !p.firstDirtyAt.IsZero() {
		cp.FirstDirtyAt = p.firstDirtyAt.UTC().Format(time.RFC3339Nano)
	}
	if !p.lastDigestAt.IsZero() {
		cp.LastDigestAt = p.lastDigestAt.UTC().Format(time.RFC3339Nano)
	}
	p.mu.Unlock()
	raw, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return store.DB.SetCheckpoint(ctx, lease, presenceCheckpointID(lease.AgentID), raw, json.RawMessage(`{"version":1}`))
}

type unreadConversationEntry struct {
	workspaceID    string
	conversationID string
	name           string
	convType       string
	unreadCount    int
	mentionCount   int
	lastSeq        int64
	dirty          bool
}

type conversationPresenceState struct {
	persistMu            sync.Mutex
	mu                   sync.Mutex
	activeWorkspaceID    string
	activeConversationID string
	activeConvType       string
	unread               map[string]*unreadConversationEntry
	firstDirtyAt         time.Time
	lastDigestAt         time.Time
	digestInterval       time.Duration
	onChange             func()
}

func newConversationPresenceState(interval time.Duration, onChange func()) *conversationPresenceState {
	if interval <= 0 {
		interval = defaultUnreadDigestInterval
	}
	return &conversationPresenceState{
		unread:         map[string]*unreadConversationEntry{},
		digestInterval: interval,
		onChange:       onChange,
	}
}

func (p *conversationPresenceState) setOnChange(fn func()) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.onChange = fn
	p.mu.Unlock()
}

func (p *conversationPresenceState) notifyChange() {
	if p == nil {
		return
	}
	p.mu.Lock()
	fn := p.onChange
	p.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (p *conversationPresenceState) Active() (string, string, string) {
	if p == nil {
		return "", "", ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeWorkspaceID, p.activeConversationID, p.activeConvType
}

func (p *conversationPresenceState) ActiveConversationID() string {
	_, convID, _ := p.Active()
	return convID
}

func (p *conversationPresenceState) Enter(workspaceID, conversationID, convType string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	changed := p.activeWorkspaceID != workspaceID || p.activeConversationID != conversationID || p.activeConvType != convType
	p.activeWorkspaceID = workspaceID
	p.activeConversationID = conversationID
	p.activeConvType = convType
	delete(p.unread, conversationID)
	hasDirty := false
	for _, entry := range p.unread {
		if entry.dirty && entry.unreadCount > 0 {
			hasDirty = true
			break
		}
	}
	if !hasDirty {
		p.firstDirtyAt = time.Time{}
	}
	fn := p.onChange
	p.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}

func (p *conversationPresenceState) Leave() {
	if p == nil {
		return
	}
	p.mu.Lock()
	changed := p.activeConversationID != "" || p.activeWorkspaceID != "" || p.activeConvType != ""
	p.activeWorkspaceID = ""
	p.activeConversationID = ""
	p.activeConvType = ""
	fn := p.onChange
	p.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}

func (p *conversationPresenceState) RecordUnread(workspaceID string, conv Conversation, m *Message, agentID string, now time.Time) {
	if p == nil || m == nil || !domain.ValidUUID(conv.ID) {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	wsID := workspaceID
	if wsID == "" {
		wsID = conv.WorkspaceID
	}
	mentioned := false
	for _, mention := range m.Mentions {
		if mention.UserID == agentID {
			mentioned = true
			break
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.unread[conv.ID]
	if !ok {
		entry = &unreadConversationEntry{
			workspaceID:    wsID,
			conversationID: conv.ID,
			name:           conv.Name,
			convType:       conv.Type,
		}
		p.unread[conv.ID] = entry
	} else {
		if entry.workspaceID == "" && wsID != "" {
			entry.workspaceID = wsID
		}
		if conv.Name != "" {
			entry.name = conv.Name
		}
		if conv.Type != "" {
			entry.convType = conv.Type
		}
	}
	if m.Seq > 0 && m.Seq <= entry.lastSeq {
		return
	}
	entry.unreadCount++
	if mentioned {
		entry.mentionCount++
	}
	if m.Seq > entry.lastSeq {
		entry.lastSeq = m.Seq
	}
	entry.dirty = true
	if p.firstDirtyAt.IsZero() {
		p.firstDirtyAt = now
	}
}

// ReconcileUnread drops revoked sources before composing another digest. Leaving
// one revoked conversation in the buffer would reject every other source in it.
func (p *conversationPresenceState) ReconcileUnread(conversations []Conversation) bool {
	allowed := make(map[string]string, len(conversations))
	for _, c := range conversations {
		allowed[c.ID] = c.WorkspaceID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	changed, dirty := false, false
	for id, entry := range p.unread {
		if ws, ok := allowed[id]; !ok || ws != entry.workspaceID {
			delete(p.unread, id)
			changed = true
			continue
		}
		dirty = dirty || entry.dirty && entry.unreadCount > 0
	}
	if changed {
		// An already queued digest may contain the removed source and fail its
		// later ACL check. Re-announce surviving unread conversations separately.
		for _, entry := range p.unread {
			entry.dirty = entry.unreadCount > 0
			dirty = dirty || entry.dirty
		}
		if dirty && p.firstDirtyAt.IsZero() {
			p.firstDirtyAt = time.Now().UTC()
		}
	}
	if !dirty {
		p.firstDirtyAt = time.Time{}
	}
	return changed
}

func (p *conversationPresenceState) EnrichConversation(c Conversation) Conversation {
	if p == nil {
		return c
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeConversationID != "" && c.ID == p.activeConversationID {
		c.Active = true
	}
	if entry, ok := p.unread[c.ID]; ok && entry.unreadCount > 0 {
		c.UnreadCount = entry.unreadCount
		c.MentionCount = entry.mentionCount
		c.LastSeq = entry.lastSeq
	}
	return c
}

func (p *conversationPresenceState) FlushDueDigest(ctx context.Context, store *Store, lease *Lease, now time.Time) (bool, error) {
	if p == nil || store == nil || store.DB == nil || lease == nil {
		return false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	p.mu.Lock()
	hasDirty := false
	for _, entry := range p.unread {
		if entry.dirty && entry.unreadCount > 0 && domain.ValidUUID(entry.workspaceID) && domain.ValidUUID(entry.conversationID) {
			hasDirty = true
			break
		}
	}
	if !hasDirty {
		p.firstDirtyAt = time.Time{}
		p.mu.Unlock()
		return false, nil
	}
	dueAt := p.firstDirtyAt.Add(p.digestInterval)
	if !p.lastDigestAt.IsZero() {
		if nextAfterLast := p.lastDigestAt.Add(p.digestInterval); nextAfterLast.After(dueAt) {
			dueAt = nextAfterLast
		}
	}
	if now.Before(dueAt) {
		p.mu.Unlock()
		return false, nil
	}

	activeConvID := p.activeConversationID
	summaries := make([]UnreadConversationSummary, 0, len(p.unread))
	snapCounts := make(map[string]int, len(p.unread))
	for _, entry := range p.unread {
		if entry.unreadCount <= 0 || !domain.ValidUUID(entry.workspaceID) || !domain.ValidUUID(entry.conversationID) {
			continue
		}
		summaries = append(summaries, UnreadConversationSummary{
			WorkspaceID:    entry.workspaceID,
			ConversationID: entry.conversationID,
			Name:           entry.name,
			Type:           entry.convType,
			UnreadCount:    entry.unreadCount,
			MentionCount:   entry.mentionCount,
			LastSeq:        entry.lastSeq,
		})
		snapCounts[entry.conversationID] = entry.unreadCount
	}
	p.mu.Unlock()

	if len(summaries) == 0 {
		return false, nil
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].ConversationID < summaries[j].ConversationID
	})
	sourceRefs := make([]InputSource, 0, len(summaries))
	for _, item := range summaries {
		sourceRefs = append(sourceRefs, InputSource{
			WorkspaceID:    item.WorkspaceID,
			ConversationID: item.ConversationID,
		})
	}
	bodyBytes, err := json.Marshal(UnreadDigestBody{
		ActiveConversationID: activeConvID,
		UnreadConversations:  summaries,
	})
	if err != nil {
		return false, err
	}
	bodyStr := string(bodyBytes)

	inserted := false
	err = store.DB.WithinFencedTx(ctx, lease, func(txCtx context.Context) error {
		var existing int
		if err := store.DB.QueryRow(
			txCtx,
			`SELECT count(*) FROM runtime_inputs WHERE agent_id=$1 AND state IN ('pending','processing') AND payload->>'kind'='im.unread_digest' AND payload->>'body'=$2`,
			lease.AgentID,
			bodyStr,
		).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		var seq string
		if err := store.DB.QueryRow(txCtx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&seq); err != nil {
			return err
		}
		notifID := "unread-digest:" + uuid.NewString()
		in := Input{
			EventID:        seq,
			Kind:           "im.unread_digest",
			NotificationID: notifID,
			Body:           bodyStr,
			CreatedAt:      now.Format(time.RFC3339Nano),
			SourceRefs:     sourceRefs,
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		tag, err := store.DB.Exec(
			txCtx,
			`INSERT INTO runtime_inputs(agent_id,event_id,payload,notification_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			lease.AgentID,
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
	if err != nil {
		return false, err
	}

	p.mu.Lock()
	stillDirty := false
	for convID, entry := range p.unread {
		if flushedCount, ok := snapCounts[convID]; ok && entry.unreadCount <= flushedCount {
			entry.dirty = false
		}
		if entry.dirty && entry.unreadCount > 0 {
			stillDirty = true
		}
	}
	if stillDirty {
		p.firstDirtyAt = now
	} else {
		p.firstDirtyAt = time.Time{}
	}
	p.lastDigestAt = now
	p.mu.Unlock()
	_ = p.Persist(ctx, store, lease)

	if inserted {
		observability.L(ctx).Info(
			"Agent unread digest persisted",
			zap.String("event.action", "agent-unread-digest-persisted"),
			zap.String("custom.agent.id", lease.AgentID),
			zap.Int("custom.im.unread_conversation_count", len(summaries)),
		)
	}
	return inserted, nil
}

type enterConversationArgs struct {
	ConversationID string `json:"conversationId" jsonschema:"required,description=Target group, channel, or direct conversationId to enter and make your active conversation"`
	Limit          int    `json:"limit,omitempty" jsonschema:"description=Number of recent messages to fetch upon entering (1 to 50, default 20)"`
}

type leaveConversationArgs struct{}

type enterConversationResult struct {
	ActiveConversationID string    `json:"activeConversationId"`
	WorkspaceID          string    `json:"workspaceId"`
	Type                 string    `json:"type"`
	Name                 string    `json:"name,omitempty"`
	Messages             []Message `json:"messages"`
	HasMore              bool      `json:"hasMore"`
	NextBeforeSeq        int64     `json:"nextBeforeSeq,omitempty"`
}

func (t *Tools) initRunPresence(ctx context.Context, run *Run) (bool, error) {
	if t == nil || t.Store == nil || t.Store.DB == nil || run == nil {
		return false, nil
	}
	if run.resume != nil {
		return t.restoreCheckpointPresence(ctx, run)
	}
	rawSnap, found, err := t.Store.DB.ReadObservation(ctx, run.ID, presenceInitObservationCallID, presenceInitObservationKind)
	if err != nil {
		return false, err
	}
	if found {
		var snap presenceInitSnapshot
		if err := json.Unmarshal(rawSnap, &snap); err != nil {
			return false, ErrReplayConflict
		}
		run.setPresenceEnabled(true)
		run.setActiveConversation(snap.WorkspaceID, snap.ConversationID, snap.ConvType)
		return true, nil
	}
	existingModels, err := t.Store.DB.ListModelCallResponses(ctx, run.ID)
	if err != nil {
		return false, err
	}
	if len(existingModels) > 0 {
		return false, nil
	}
	if t.Presence == nil || (t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "im.marketplace.enabled")) {
		return false, nil
	}
	wsID, convID, convType := t.Presence.Active()
	snapBytes, err := json.Marshal(presenceInitSnapshot{
		WorkspaceID:    wsID,
		ConversationID: convID,
		ConvType:       convType,
	})
	if err != nil {
		return false, err
	}
	if err := t.Store.DB.SaveObservation(ctx, t.Lease, run.ID, presenceInitObservationCallID, presenceInitObservationKind, snapBytes); err != nil {
		return false, err
	}
	run.setPresenceEnabled(true)
	run.setActiveConversation(wsID, convID, convType)
	return true, nil
}

func (t *Tools) presenceEnforced(run *Run) bool {
	if run != nil && run.isPresenceEnabled() {
		return true
	}
	return t != nil && t.Presence != nil
}

func (t *Tools) activeConversation(run *Run) (string, string, string) {
	if run != nil && run.isPresenceEnabled() {
		return run.activeConversation()
	}
	if t != nil && t.Presence != nil {
		return t.Presence.Active()
	}
	return "", "", ""
}

func (t *Tools) checkSendPresence(run *Run, conversationID string) *Observation {
	if !t.presenceEnforced(run) {
		return nil
	}
	_, activeConvID, _ := t.activeConversation(run)
	if activeConvID != conversationID {
		return &Observation{OK: false, Error: "not_in_conversation"}
	}
	return nil
}

func (t *Tools) applyEnterConversation(ctx context.Context, run *Run, workspaceID, conversationID, convType string) {
	if run != nil {
		run.setActiveConversation(workspaceID, conversationID, convType)
		run.setMCPTools(nil)
	}
	if t != nil && t.Presence != nil {
		t.Presence.Enter(workspaceID, conversationID, convType)
		_ = t.Presence.Persist(ctx, t.Store, t.Lease)
	}
}

func (t *Tools) applyLeaveConversation(ctx context.Context, run *Run) {
	if run != nil {
		run.setActiveConversation("", "", "")
		run.setMCPTools(nil)
	}
	if t != nil && t.Presence != nil {
		t.Presence.Leave()
		_ = t.Presence.Persist(ctx, t.Store, t.Lease)
	}
}

func (t *Tools) applyReplayPresence(ctx context.Context, run *Run, name string, cached []byte) {
	if !t.presenceEnforced(run) {
		return
	}
	switch name {
	case "im_enter_conversation":
		var receipt struct {
			Data enterConversationResult `json:"data"`
		}
		if json.Unmarshal(cached, &receipt) == nil && validUUID(receipt.Data.ActiveConversationID) {
			t.applyEnterConversation(ctx, run, receipt.Data.WorkspaceID, receipt.Data.ActiveConversationID, receipt.Data.Type)
		}
	case "im_leave_conversation":
		t.applyLeaveConversation(ctx, run)
	case "im_open_direct_conversation", "im_create_group_conversation":
		var receipt struct {
			Data Conversation `json:"data"`
		}
		if json.Unmarshal(cached, &receipt) == nil && validUUID(receipt.Data.ID) {
			convType := receipt.Data.Type
			if convType == "" {
				if name == "im_open_direct_conversation" {
					convType = "direct"
				} else {
					convType = "group"
				}
			}
			t.applyEnterConversation(ctx, run, receipt.Data.WorkspaceID, receipt.Data.ID, convType)
		}
	}
}

// BuildPresenceTools constructs im_enter_conversation and im_leave_conversation for unified
// conversation presence across group, channel, and direct conversations.
func (t *Tools) BuildPresenceTools() ([]tool.BaseTool, error) {
	enterTool, err := utils.InferTool(
		"im_enter_conversation",
		"Enter a group, channel, or direct conversation as your single active conversation, load its recent messages, advance your IM read cursor there, and (when entering a group with installed Marketplace apps) dynamically mount its mcp__<slug>__<tool> tools for the next step of this Turn.",
		func(ctx context.Context, args *enterConversationArgs) (*Observation, error) {
			run, err := CurrentRun(ctx)
			if err != nil {
				return nil, err
			}
			return t.execute(ctx, "im_enter_conversation", args, func(ctx context.Context, _ string) (*Observation, error) {
				if args == nil || !validUUID(args.ConversationID) || args.Limit < 0 || args.Limit > 50 {
					return &Observation{Error: "invalid_arguments"}, nil
				}
				limit := args.Limit
				if limit == 0 {
					limit = 20
				}
				b, err := t.IM.Bootstrap(ctx)
				if err != nil {
					return observeError(err), nil
				}
				var target *Conversation
				for i := range b.Conversations {
					if b.Conversations[i].ID == args.ConversationID {
						target = &b.Conversations[i]
						break
					}
				}
				if target == nil || !validUUID(target.WorkspaceID) {
					return &Observation{Error: "conversation_not_found"}, nil
				}
				q := url.Values{
					"limit":     {strconv.Itoa(limit)},
					"beforeSeq": {"0"},
				}
				var page struct {
					Messages []Message `json:"messages"`
					HasMore  bool      `json:"hasMore"`
				}
				if err := t.IM.Call(ctx, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(target.ID)+"/messages?"+q.Encode(), nil, &page); err != nil {
					return observeError(err), nil
				}
				outMsgs, cursor, more := historyPage(page.Messages, page.HasMore, "")
				var maxSeq int64
				for _, m := range page.Messages {
					if m.Seq > maxSeq {
						maxSeq = m.Seq
					}
				}
				if maxSeq > 0 {
					_ = t.IM.MarkRead(ctx, target.ID, maxSeq)
				}
				t.applyEnterConversation(ctx, run, target.WorkspaceID, target.ID, target.Type)
				return &Observation{
					OK: true,
					Data: enterConversationResult{
						ActiveConversationID: target.ID,
						WorkspaceID:          target.WorkspaceID,
						Type:                 target.Type,
						Name:                 target.Name,
						Messages:             outMsgs,
						HasMore:              more,
						NextBeforeSeq:        cursor,
					},
				}, nil
			})
		},
		utils.WithMarshalOutput(marshalObservation),
	)
	if err != nil {
		return nil, err
	}

	leaveTool, err := utils.InferTool(
		"im_leave_conversation",
		"Leave your current active conversation so no conversation is active, unmounting any active group Marketplace MCP tools and closing its live notification stream.",
		func(ctx context.Context, args *leaveConversationArgs) (*Observation, error) {
			run, err := CurrentRun(ctx)
			if err != nil {
				return nil, err
			}
			if args == nil {
				args = &leaveConversationArgs{}
			}
			return t.execute(ctx, "im_leave_conversation", args, func(execCtx context.Context, _ string) (*Observation, error) {
				t.applyLeaveConversation(execCtx, run)
				return &Observation{
					OK:   true,
					Data: map[string]any{"activeConversationId": ""},
				}, nil
			})
		},
		utils.WithMarshalOutput(marshalObservation),
	)
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{enterTool, leaveTool}, nil
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

func (t *Tools) discoverConversationSpecs(ctx context.Context, wsID, convID string, usedNames map[string]struct{}) ([]mcpPinnedToolSpec, error) {
	if !domain.ValidUUID(convID) || t == nil || t.IM == nil || strings.TrimSpace(t.IM.BaseURL) == "" {
		return nil, nil
	}
	if !domain.ValidUUID(wsID) {
		if b, err := t.IM.Bootstrap(ctx); err == nil {
			for _, c := range b.Conversations {
				if c.ID == convID {
					wsID = c.WorkspaceID
					break
				}
			}
		}
	}
	var appsOut struct {
		Apps []imMarketplaceApp `json:"apps"`
	}
	if err := t.IM.Call(ctx, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(convID)+"/apps", nil, &appsOut); err != nil {
		var api *APIError
		if errors.As(err, &api) && (api.Status == 400 || api.Status == 403 || api.Status == 404) {
			return nil, nil
		}
		return nil, err
	}
	if len(appsOut.Apps) == 0 {
		return nil, nil
	}
	sort.Slice(appsOut.Apps, func(i, j int) bool {
		if appsOut.Apps[i].Slug != appsOut.Apps[j].Slug {
			return appsOut.Apps[i].Slug < appsOut.Apps[j].Slug
		}
		return appsOut.Apps[i].ID < appsOut.Apps[j].ID
	})
	specs := []mcpPinnedToolSpec{}
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
	return specs, nil
}

func (t *Tools) discoverOrRestoreConversationMCPTools(ctx context.Context, run *Run, wsID, convID string) ([]*mcpToolWrapper, error) {
	if t == nil || t.Store == nil || t.Store.DB == nil || run == nil || !domain.ValidUUID(convID) {
		return nil, nil
	}
	obsCallID := mcpToolsObservationCallID + ":" + convID
	rawSnap, found, err := t.Store.DB.ReadObservation(ctx, run.ID, obsCallID, mcpToolsObservationKind)
	if err != nil {
		return nil, err
	}
	if found {
		var specs []mcpPinnedToolSpec
		if err := json.Unmarshal(rawSnap, &specs); err != nil {
			return nil, ErrReplayConflict
		}
		out := make([]*mcpToolWrapper, 0, len(specs))
		for _, spec := range specs {
			if spec.ToolInfo == nil || spec.ExposedName == "" {
				return nil, ErrReplayConflict
			}
			out = append(out, &mcpToolWrapper{tools: t, spec: spec})
		}
		return out, nil
	}

	specs := []mcpPinnedToolSpec{}
	if (t.FlagsFile == "" || runtimeFlag(t.FlagsFile, "im.marketplace.enabled")) && t.IM != nil && strings.TrimSpace(t.IM.BaseURL) != "" {
		discovered, err := t.discoverConversationSpecs(ctx, wsID, convID, map[string]struct{}{})
		if err != nil {
			return nil, err
		}
		if len(discovered) > 0 {
			specs = discovered
		}
	}
	snapBytes, err := json.Marshal(specs)
	if err != nil {
		return nil, err
	}
	if err := t.Store.DB.SaveObservation(ctx, t.Lease, run.ID, obsCallID, mcpToolsObservationKind, snapBytes); err != nil {
		return nil, err
	}
	out := make([]*mcpToolWrapper, 0, len(specs))
	for _, spec := range specs {
		out = append(out, &mcpToolWrapper{tools: t, spec: spec})
	}
	return out, nil
}

type mcpDynamicToolMiddleware struct {
	*adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]
	tools *Tools
}

// NewMCPDynamicToolMiddleware mounts or unmounts the active group conversation's MCP tools
// in state.ToolInfos before every model call within a Turn.
func (t *Tools) NewMCPDynamicToolMiddleware() adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] {
	return &mcpDynamicToolMiddleware{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]{},
		tools:                             t,
	}
}

func (m *mcpDynamicToolMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.TypedChatModelAgentState[*schema.AgenticMessage],
	_ *adk.TypedModelContext[*schema.AgenticMessage],
) (context.Context, *adk.TypedChatModelAgentState[*schema.AgenticMessage], error) {
	if state == nil || m == nil || m.tools == nil {
		return ctx, state, nil
	}
	run, err := CurrentRun(ctx)
	if err != nil || run == nil {
		return ctx, state, nil
	}
	baseInfos := make([]*schema.ToolInfo, 0, len(state.ToolInfos))
	for _, info := range state.ToolInfos {
		if info != nil && strings.HasPrefix(info.Name, "mcp__") {
			continue
		}
		baseInfos = append(baseInfos, info)
	}
	wsID, convID, convType := m.tools.activeConversation(run)
	if convID == "" || convType != "group" {
		run.setMCPTools(nil)
		if len(baseInfos) == len(state.ToolInfos) {
			return ctx, state, nil
		}
		copyState := *state
		copyState.ToolInfos = baseInfos
		return ctx, &copyState, nil
	}
	wrappers, err := m.tools.discoverOrRestoreConversationMCPTools(ctx, run, wsID, convID)
	if err != nil {
		return ctx, nil, err
	}
	toolMap := make(map[string]*mcpToolWrapper, len(wrappers))
	for _, w := range wrappers {
		info, infoErr := w.Info(ctx)
		if infoErr != nil {
			return ctx, nil, infoErr
		}
		if info != nil && info.Name != "" {
			baseInfos = append(baseInfos, info)
			toolMap[info.Name] = w
		}
	}
	run.setMCPTools(toolMap)
	copyState := *state
	copyState.ToolInfos = baseInfos
	return ctx, &copyState, nil
}

// HandleUnknownMCPTool dispatches dynamically mounted mcp__<slug>__<tool> invocations via
// compose.ToolsNodeConfig.UnknownToolsHandler while preserving durable receipts.
func (t *Tools) HandleUnknownMCPTool(ctx context.Context, name, input string) (string, error) {
	run, err := CurrentRun(ctx)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(name, "mcp__") {
		wsID, convID, convType := t.activeConversation(run)
		if convID != "" && convType == "group" {
			if wrapper := run.getMCPTool(name); wrapper != nil && wrapper.spec.ConversationID == convID && wrapper.spec.WorkspaceID == wsID {
				return wrapper.InvokableRun(ctx, input)
			}
			wrappers, discoverErr := t.discoverOrRestoreConversationMCPTools(ctx, run, wsID, convID)
			if discoverErr == nil {
				toolMap := make(map[string]*mcpToolWrapper, len(wrappers))
				for _, w := range wrappers {
					if w.spec.ExposedName != "" {
						toolMap[w.spec.ExposedName] = w
					}
				}
				run.setMCPTools(toolMap)
				if wrapper := toolMap[name]; wrapper != nil {
					return wrapper.InvokableRun(ctx, input)
				}
			}
		}
	}
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		obs, execErr := t.execute(ctx, name, trimmed, func(context.Context, string) (*Observation, error) {
			return &Observation{OK: false, Error: "unknown_tool"}, nil
		})
		if execErr != nil {
			return "", execErr
		}
		return marshalObservation(ctx, obs)
	}
	obs, execErr := t.execute(ctx, name, json.RawMessage(trimmed), func(context.Context, string) (*Observation, error) {
		return &Observation{OK: false, Error: "unknown_tool"}, nil
	})
	if execErr != nil {
		return "", execErr
	}
	return marshalObservation(ctx, obs)
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
		discovered, err := t.discoverConversationSpecs(ctx, convWorkspaces[convID], convID, usedNames)
		if err != nil {
			return nil, err
		}
		specs = append(specs, discovered...)
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
	cli, err := newMCPHTTPClient(discoverCtx, sess.App.MCPURL, headers)
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
	if _, exists := used[base]; !exists && len(base) <= 64 {
		used[base] = struct{}{}
		return base
	}
	// Include the original identity, not its lossy normalized spelling or a
	// conversation prefix. Check every candidate, including hash collisions.
	identity, _ := json.Marshal([]string{slug, remoteName, convID})
	for attempt := 0; ; attempt++ {
		suffix := "__" + digest([]byte(fmt.Sprintf("%s/%d", identity, attempt)))[:16]
		candidate := base[:min(len(base), 64-len(suffix))] + suffix
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
	}
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

	obs, err := w.tools.execute(ctx, w.spec.ExposedName, json.RawMessage(trimmed), func(execCtx context.Context, requestID string) (*Observation, error) {
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
		cli, err := newMCPHTTPClient(callCtx, mcpURL, headers)
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

		receivedResult := false
		einoTools, err := einomcp.GetTools(callCtx, &einomcp.Config{
			Cli:           cli,
			ToolNameList:  []string{w.spec.RemoteName},
			CustomHeaders: headers,
			ToolCallResultHandler: func(_ context.Context, _ string, result *mcp.CallToolResult) (*mcp.CallToolResult, error) {
				receivedResult = true
				return result, nil
			},
		})
		if err != nil || len(einoTools) == 0 {
			return &Observation{Error: "mcp_tool_not_found"}, nil
		}
		invokable, ok := einoTools[0].(tool.InvokableTool)
		if !ok {
			return &Observation{Error: "mcp_tool_not_found"}, nil
		}
		callOpts := append(append([]tool.Option{}, opts...), einomcp.WithCustomHeaders(map[string]string{
			"Authorization":   headers["Authorization"],
			"Idempotency-Key": requestID,
		}))
		rawOut, err := invokable.InvokableRun(callCtx, trimmed, callOpts...)
		if err != nil {
			code := "mcp_tool_error"
			if !receivedResult {
				code = "mcp_result_unknown"
			}
			return &Observation{
				OK:    false,
				Error: code,
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

func (e *Engine) authorizeUnreadDigestInput(ctx context.Context, imc *IM, _ *Lease, in Input) error {
	if len(in.SourceRefs) == 0 {
		return errContextUnavailable
	}
	if e.Config.FlagsFile != "" && !runtimeFlag(e.Config.FlagsFile, "im.marketplace.enabled") {
		return errContextUnavailable
	}
	var body UnreadDigestBody
	if err := json.Unmarshal([]byte(in.Body), &body); err != nil || len(body.UnreadConversations) == 0 {
		return errContextUnavailable
	}
	b, err := imc.Bootstrap(ctx)
	if err != nil {
		var api *APIError
		if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 {
			return errContextUnavailable
		}
		return err
	}
	convWorkspaces := make(map[string]string, len(b.Conversations))
	for _, c := range b.Conversations {
		convWorkspaces[c.ID] = c.WorkspaceID
	}
	for _, ref := range in.SourceRefs {
		if !domain.ValidUUID(ref.ConversationID) {
			return errContextUnavailable
		}
		wsID, ok := convWorkspaces[ref.ConversationID]
		if !ok || (ref.WorkspaceID != "" && wsID != ref.WorkspaceID) {
			return errContextUnavailable
		}
	}
	return nil
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
	ctx         context.Context
	cancel      context.CancelFunc
	lost        atomic.Bool
}

type mcpSubscriptionRetry struct {
	url   string
	after time.Time
	delay time.Duration
}

type mcpNotificationWatcher struct {
	store     *Store
	lease     *Lease
	im        *IM
	flagsFile string
	presence  *conversationPresenceState
	wakeCh    chan struct{}
	syncCh    chan struct{}
	now       func() time.Time

	scopeMu     sync.Mutex
	scope       presenceInitSnapshot
	scopeCtx    context.Context
	scopeCancel context.CancelFunc

	mu      sync.Mutex
	subs    map[mcpWatchKey]*mcpActiveSubscription
	retries map[mcpWatchKey]mcpSubscriptionRetry
}

func newMCPNotificationWatcher(store *Store, lease *Lease, im *IM, flagsFile string) *mcpNotificationWatcher {
	return &mcpNotificationWatcher{
		store:     store,
		lease:     lease,
		im:        im,
		flagsFile: flagsFile,
		wakeCh:    make(chan struct{}, 1),
		syncCh:    make(chan struct{}, 1),
		now:       time.Now,
		subs:      map[mcpWatchKey]*mcpActiveSubscription{},
		retries:   map[mcpWatchKey]mcpSubscriptionRetry{},
	}
}

// start coordinates external subscriptions independently of inbox ingestion and
// TurnLoop delivery. This goroutine only manages transports; notifications still
// enter through the fenced inbox and never mutate the Agent Session directly.
func (w *mcpNotificationWatcher) start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer w.closeAll()
		defer func() {
			w.scopeMu.Lock()
			defer w.scopeMu.Unlock()
			if w.scopeCancel != nil {
				w.scopeCancel()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.syncCh:
				w.sync(w.contextForScope(ctx))
			}
		}
	}()
	w.requestSync()
	return func() { cancel(); <-done }
}

func (w *mcpNotificationWatcher) currentScope() presenceInitSnapshot {
	ws, conv, typ := w.presence.Active()
	return presenceInitSnapshot{WorkspaceID: ws, ConversationID: conv, ConvType: typ}
}

func (w *mcpNotificationWatcher) contextForScope(ctx context.Context) context.Context {
	w.scopeMu.Lock()
	defer w.scopeMu.Unlock()
	scope := w.currentScope()
	if w.scopeCtx == nil || w.scopeCtx.Err() != nil || w.scope != scope {
		if w.scopeCancel != nil {
			w.scopeCancel()
		}
		w.scope = scope
		w.scopeCtx, w.scopeCancel = context.WithCancel(ctx)
	}
	return w.scopeCtx
}

func (w *mcpNotificationWatcher) requestSync() {
	// A switch cancels even an in-flight Initialize immediately. Reconciliation
	// requests for the same scope coalesce without restarting healthy streams.
	w.scopeMu.Lock()
	if w.scopeCancel != nil && w.scope != w.currentScope() {
		w.scopeCancel()
	}
	w.scopeMu.Unlock()
	select {
	case w.syncCh <- struct{}{}:
	default:
	}
}

func (w *mcpNotificationWatcher) presenceChanged() {
	w.requestSync()
	w.triggerWake()
}

func (w *mcpNotificationWatcher) setPresence(p *conversationPresenceState) {
	if w == nil {
		return
	}
	w.presence = p
}

func (w *mcpNotificationWatcher) triggerWake() {
	if w == nil {
		return
	}
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *mcpNotificationWatcher) wake() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.wakeCh
}

func (w *mcpNotificationWatcher) activeSubscriptionCount() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.subs)
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
	clear(w.retries)
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

	groupConvs := make([]Conversation, 0, 1)
	if w.presence != nil {
		wsID, activeConvID, activeConvType := w.presence.Active()
		if activeConvID == "" || activeConvType != "group" || !domain.ValidUUID(activeConvID) {
			w.closeAll()
			return
		}
		if !domain.ValidUUID(wsID) {
			var b Bootstrap
			if err := w.im.Call(ctx, http.MethodGet, "/api/v1/bootstrap", nil, &b); err != nil {
				return
			}
			for _, c := range b.Conversations {
				if c.ID == activeConvID && c.Type == "group" && domain.ValidUUID(c.WorkspaceID) {
					wsID = c.WorkspaceID
					break
				}
			}
			if !domain.ValidUUID(wsID) {
				w.closeAll()
				return
			}
		}
		groupConvs = append(groupConvs, Conversation{
			ID:          activeConvID,
			WorkspaceID: wsID,
			Type:        "group",
		})
	} else {
		var b Bootstrap
		if err := w.im.Call(ctx, http.MethodGet, "/api/v1/bootstrap", nil, &b); err != nil {
			return
		}
		for _, c := range b.Conversations {
			if c.Type == "group" && domain.ValidUUID(c.ID) && domain.ValidUUID(c.WorkspaceID) {
				groupConvs = append(groupConvs, c)
			}
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
			var api *APIError
			if errors.As(err, &api) && (api.Status == 400 || api.Status == 403 || api.Status == 404) {
				continue
			}
			if w.presence != nil {
				return
			}
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

	now := w.now().UTC()
	var toClose []*mcpActiveSubscription
	w.mu.Lock()
	for key, retry := range w.retries {
		if target, ok := desired[key]; !ok || target.app.MCPURL != retry.url {
			delete(w.retries, key)
		}
	}
	for key, sub := range w.subs {
		target, ok := desired[key]
		if !ok || sub.lost.Load() || sub.ctx.Err() != nil || sub.app.MCPURL != target.app.MCPURL || (!sub.expiresAt.IsZero() && now.Add(time.Minute).After(sub.expiresAt)) {
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
		retry := w.retries[key]
		w.mu.Unlock()
		if exists || now.Before(retry.after) {
			continue
		}
		started := w.startSubscription(ctx, key, target.workspaceID, target.convID, target.app)
		if ctx.Err() != nil {
			return
		}
		w.mu.Lock()
		if started {
			delete(w.retries, key)
		} else {
			delay := min(max(time.Second, retry.delay*2), 30*time.Second)
			w.retries[key] = mcpSubscriptionRetry{url: target.app.MCPURL, after: w.now().Add(delay), delay: delay}
		}
		w.mu.Unlock()
	}
}

func (w *mcpNotificationWatcher) startSubscription(ctx context.Context, key mcpWatchKey, workspaceID, convID string, app imMarketplaceApp) bool {
	var sessOut imConversationAppSession
	if err := w.im.Call(ctx, http.MethodPost, "/api/v1/conversations/"+url.PathEscape(convID)+"/apps/"+url.PathEscape(app.ID)+"/session", map[string]any{}, &sessOut); err != nil {
		return false
	}
	expiresAt, _ := time.Parse(time.RFC3339, sessOut.ExpiresAt)
	headers := map[string]string{
		"Authorization": "Bearer " + sessOut.Token,
	}
	mcpURL := sessOut.App.MCPURL
	if mcpURL == "" {
		mcpURL = app.MCPURL
	}
	subCtx, cancel := context.WithCancel(ctx)
	sub := &mcpActiveSubscription{
		key:         key,
		workspaceID: workspaceID,
		app:         app,
		expiresAt:   expiresAt,
		ctx:         subCtx,
		cancel:      cancel,
	}
	cli, err := newMCPHTTPClientWithInvalidate(
		subCtx,
		mcpURL,
		headers,
		func() {
			if sub.lost.CompareAndSwap(false, true) {
				w.requestSync()
				w.triggerWake()
			}
		},
		transport.WithContinuousListening(),
	)
	if err != nil {
		cancel()
		return false
	}
	sub.cli = cli
	cli.OnNotification(func(n mcp.JSONRPCNotification) {
		w.handleNotification(subCtx, workspaceID, convID, app, n)
	})

	if err := cli.Start(subCtx); err != nil {
		cancel()
		_ = cli.Close()
		return false
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
		return false
	}

	w.mu.Lock()
	if ctx.Err() != nil {
		w.mu.Unlock()
		cancel()
		_ = cli.Close()
		return false
	}
	w.subs[key] = sub
	w.mu.Unlock()
	return true
}

func (w *mcpNotificationWatcher) handleNotification(ctx context.Context, workspaceID, convID string, app imMarketplaceApp, n mcp.JSONRPCNotification) {
	if w.presence != nil && w.presence.ActiveConversationID() != convID {
		return
	}
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
			SourceRefs:     []InputSource{{WorkspaceID: workspaceID, ConversationID: convID}},
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
		w.triggerWake()
	}
}
