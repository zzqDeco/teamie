package http

import (
	"context"
	"time"

	chatgen "teamie/contracts/gen/im/hertz_gen/im/chat"
	"teamie/services/chat/internal/domain"
)

func toAnnouncementDTO(a domain.Announcement) *chatgen.AnnouncementDTO {
	return &chatgen.AnnouncementDTO{
		ID:         a.ID,
		Seq:        a.Seq,
		Title:      a.Title,
		Body:       a.Body,
		AuthorId:   a.AuthorID,
		AuthorName: a.AuthorName,
		CreatedAt:  a.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (a *Adapter) ListAnnouncements(ctx context.Context, req *chatgen.ListAnnouncementsReq) (*chatgen.ListAnnouncementsResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	beforeSeq, err := parseIntParam(req.GetBeforeSeq(), "beforeSeq", 0, 1, 1<<62)
	if err != nil {
		return nil, err
	}
	limit, err := parseIntParam(req.GetLimit(), "limit", 50, 1, 100)
	if err != nil {
		return nil, err
	}
	page, err := a.App.ListAnnouncements(ctx, user.ID, beforeSeq, int(limit))
	if err != nil {
		return nil, err
	}
	items := make([]*chatgen.AnnouncementDTO, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toAnnouncementDTO(item))
	}
	return &chatgen.ListAnnouncementsResp{
		Items:     items,
		LatestSeq: page.LatestSeq,
		ReadSeq:   page.ReadSeq,
		HasMore:   page.HasMore,
	}, nil
}

func (a *Adapter) MarkAnnouncementsRead(ctx context.Context, req *chatgen.MarkAnnouncementsReadReq) (*chatgen.MarkAnnouncementsReadResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	readSeq, err := a.App.MarkAnnouncementsRead(ctx, user.ID, req.GetSeq())
	if err != nil {
		return nil, err
	}
	return &chatgen.MarkAnnouncementsReadResp{ReadSeq: readSeq}, nil
}

func (a *Adapter) CreateAnnouncement(ctx context.Context, req *chatgen.CreateAnnouncementReq) (*chatgen.AnnouncementResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	created, err := a.App.CreateAnnouncement(ctx, user, req.GetTitle(), req.GetBody())
	if err != nil {
		return nil, err
	}
	return &chatgen.AnnouncementResp{Announcement: toAnnouncementDTO(created)}, nil
}

func (a *Adapter) DeleteAnnouncement(ctx context.Context, req *chatgen.DeleteAnnouncementReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.DeleteAnnouncement(ctx, user, req.GetID()); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}
