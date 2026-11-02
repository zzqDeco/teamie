package eino

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"
	"teamie/pkg/observability"
	"teamie/services/agent-runtime/internal/domain"
)

const (
	defaultAnnouncementPollInterval = 5 * time.Second
	announcementIngestPageLimit     = 20
)

type imAnnouncement struct {
	ID         string `json:"id"`
	Seq        int64  `json:"seq"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	AuthorID   string `json:"authorId"`
	AuthorName string `json:"authorName"`
	CreatedAt  string `json:"createdAt"`
}

type imAnnouncementPage struct {
	Items     []imAnnouncement `json:"items"`
	ReadSeq   int64            `json:"readSeq"`
	LatestSeq int64            `json:"latestSeq"`
	HasMore   bool             `json:"hasMore"`
}

func formatAnnouncementInputBody(title, body string) string {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" {
		return body
	}
	if body == "" {
		return "# " + title
	}
	return "# " + title + "\n\n" + body
}

func announcementInputSource(item imAnnouncement) InputSource {
	return InputSource{
		EventID:        fmt.Sprintf("announcement:%d", item.Seq),
		WorkspaceID:    domain.PlatformAnnouncementWorkspace,
		ConversationID: domain.PlatformAnnouncementConversation,
		MessageID:      item.ID,
	}
}

func (e *Engine) ingestAnnouncements(ctx context.Context, imc *IM, l *Lease) (committed []Input, failure error) {
	if e == nil || e.Store == nil || imc == nil || l == nil {
		return nil, nil
	}
	ctx, operation := observability.Begin(ctx, "agent-announcement-ingest", zap.String("custom.agent.id", l.AgentID))
	defer func() { operation.EndQuiet(failure) }()

	var page imAnnouncementPage
	firstPath := fmt.Sprintf("/api/v1/announcements?limit=%d", announcementIngestPageLimit)
	if err := imc.Call(ctx, http.MethodGet, firstPath, nil, &page); err != nil {
		var api *APIError
		if errors.As(err, &api) && api.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	if page.LatestSeq <= page.ReadSeq || len(page.Items) == 0 {
		return nil, nil
	}

	var unread []imAnnouncement
	if page.ReadSeq == 0 {
		// Brand-new Agents receive only the current latest announcement rather than
		// replaying obsolete historical changelogs, matching human first-entry UX.
		latest := page.Items[0]
		for _, item := range page.Items[1:] {
			if item.Seq > latest.Seq {
				latest = item
			}
		}
		if latest.Seq > 0 {
			unread = []imAnnouncement{latest}
		}
	} else {
		readCursor := page.ReadSeq
		seenIDs := make(map[string]bool, len(page.Items))
		curPage := page
		for {
			var minSeq int64
			reachedReadCursor := false
			for i, item := range curPage.Items {
				if i == 0 || item.Seq < minSeq {
					minSeq = item.Seq
				}
				if item.Seq <= readCursor {
					reachedReadCursor = true
					continue
				}
				if !seenIDs[item.ID] {
					seenIDs[item.ID] = true
					unread = append(unread, item)
				}
			}
			if reachedReadCursor || !curPage.HasMore || len(curPage.Items) == 0 || minSeq <= readCursor+1 {
				break
			}
			var nextPage imAnnouncementPage
			nextPath := fmt.Sprintf("/api/v1/announcements?beforeSeq=%d&limit=%d", minSeq, announcementIngestPageLimit)
			if err := imc.Call(ctx, http.MethodGet, nextPath, nil, &nextPage); err != nil {
				return nil, err
			}
			nextMin := minSeq
			for _, item := range nextPage.Items {
				if item.Seq < nextMin {
					nextMin = item.Seq
				}
			}
			if len(nextPage.Items) == 0 || nextMin >= minSeq {
				break
			}
			curPage = nextPage
		}
		slices.SortFunc(unread, func(a, b imAnnouncement) int {
			return cmp.Compare(a.Seq, b.Seq)
		})
	}
	if len(unread) == 0 {
		return nil, nil
	}

	var maxSeq int64
	persisted := make([]Input, 0, len(unread))
	err := e.Store.DB.WithinFencedTx(ctx, l, func(txCtx context.Context) error {
		for _, item := range unread {
			if !domain.ValidUUID(item.ID) || item.Seq <= 0 {
				continue
			}
			bodyStr := formatAnnouncementInputBody(item.Title, item.Body)
			if bodyStr == "" {
				continue
			}
			sourceRefs, err := canonicalContextSources([]InputSource{announcementInputSource(item)})
			if err != nil || len(sourceRefs) == 0 {
				return err
			}
			var seq string
			if err := e.Store.DB.QueryRow(txCtx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&seq); err != nil {
				return err
			}
			notifID := "announcement:" + item.ID
			in := Input{
				EventID:        seq,
				SenderID:       item.AuthorID,
				SenderName:     item.AuthorName,
				SenderKind:     "human",
				Body:           bodyStr,
				Seq:            item.Seq,
				CreatedAt:      item.CreatedAt,
				Kind:           "platform.announcement",
				NotificationID: notifID,
				SourceRefs:     sourceRefs,
			}
			raw, err := json.Marshal(in)
			if err != nil {
				return err
			}
			tag, err := e.Store.DB.Exec(
				txCtx,
				`INSERT INTO runtime_inputs(agent_id,event_id,payload,notification_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
				l.AgentID,
				seq,
				raw,
				notifID,
			)
			if err != nil {
				return err
			}
			if item.Seq > maxSeq {
				maxSeq = item.Seq
			}
			if tag.RowsAffected() > 0 {
				persisted = append(persisted, in)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if maxSeq > 0 {
		var ack struct {
			ReadSeq int64 `json:"readSeq"`
		}
		if err := imc.Call(ctx, http.MethodPost, "/api/v1/announcements/read", map[string]any{"seq": maxSeq}, &ack); err != nil {
			return persisted, err
		}
	}
	for _, in := range persisted {
		observability.L(ctx).Info("Platform announcement persisted",
			zap.String("event.action", "platform-announcement-persisted"),
			zap.String("custom.agent.id", l.AgentID),
			zap.String("custom.input.event_id", in.EventID),
			zap.String("custom.announcement.notification_id", observability.ID(in.NotificationID)),
			zap.Int64("custom.announcement.seq", in.Seq),
		)
	}
	return persisted, nil
}

func (e *Engine) authorizeAnnouncementInput(ctx context.Context, imc *IM, lease *Lease, in Input) error {
	if strings.TrimSpace(in.Body) == "" || len(in.SourceRefs) == 0 {
		return errContextUnavailable
	}
	if in.Seq > 0 && strings.HasPrefix(in.NotificationID, "announcement:") {
		annID := strings.TrimPrefix(in.NotificationID, "announcement:")
		hasPlatformRef := false
		for _, ref := range in.SourceRefs {
			if seq, ok := domain.IsPlatformAnnouncementSource(ref); ok && seq == in.Seq && ref.MessageID == annID {
				hasPlatformRef = true
				break
			}
		}
		if !hasPlatformRef {
			var check imAnnouncementPage
			path := fmt.Sprintf("/api/v1/announcements?beforeSeq=%d&limit=1", in.Seq+1)
			if err := imc.Call(ctx, http.MethodGet, path, nil, &check); err != nil {
				var api *APIError
				if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 {
					return errContextUnavailable
				}
				return err
			}
			if len(check.Items) == 0 || check.Items[0].Seq != in.Seq || check.Items[0].ID != annID {
				return errContextUnavailable
			}
		}
	}
	refs, err := canonicalContextSources(in.SourceRefs)
	if err != nil || len(refs) == 0 {
		return errContextUnavailable
	}
	archive := &ContextArchive{Store: e.Store, Lease: lease, IM: imc}
	return archive.authorizeSources(ctx, refs)
}
