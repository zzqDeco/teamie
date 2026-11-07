package http

import (
	"context"
	"strconv"
	"time"

	chatgen "teamie/contracts/gen/im/hertz_gen/im/chat"
	"teamie/services/chat/internal/domain"
)

func (a *Adapter) PutPin(ctx context.Context, req *chatgen.PutPinReq) (*chatgen.PutPinResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	pin, version, err := a.App.PinMessage(ctx, user, req.ID, req.MessageId)
	if err != nil {
		return nil, err
	}
	return &chatgen.PutPinResp{
		Pin:         toPinDTO(pin),
		PinsVersion: strconv.FormatInt(version, 10),
	}, nil
}

func (a *Adapter) DeletePin(ctx context.Context, req *chatgen.DeletePinReq) (*chatgen.DeletePinResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	version, err := a.App.UnpinMessage(ctx, user.ID, req.ID, req.MessageId)
	if err != nil {
		return nil, err
	}
	return &chatgen.DeletePinResp{
		PinsVersion: strconv.FormatInt(version, 10),
	}, nil
}

func (a *Adapter) ListPins(ctx context.Context, req *chatgen.ListPinsReq) (*chatgen.ListPinsResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	pins, version, err := a.App.ListPins(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	dtos := make([]*chatgen.PinDTO, 0, len(pins))
	for _, p := range pins {
		dtos = append(dtos, toPinDTO(p))
	}
	return &chatgen.ListPinsResp{
		Pins:        dtos,
		PinsVersion: strconv.FormatInt(version, 10),
	}, nil
}

func (a *Adapter) SaveMessage(ctx context.Context, req *chatgen.SaveMessageReq) (*chatgen.SaveMessageResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	saved, err := a.App.SaveMessage(ctx, user.ID, req.ID, req.MessageId)
	if err != nil {
		return nil, err
	}
	return &chatgen.SaveMessageResp{
		Saved: &chatgen.SavedMarkerDTO{
			ConversationId: saved.ConversationID,
			MessageId:      saved.MessageID,
			SavedAt:        saved.SavedAt.UTC().Format(time.RFC3339Nano),
		},
	}, nil
}

func (a *Adapter) UnsaveMessage(ctx context.Context, req *chatgen.UnsaveMessageReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.UnsaveMessage(ctx, user.ID, req.ID, req.MessageId); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}

func (a *Adapter) ListSaved(ctx context.Context, req *chatgen.ListSavedReq) (*chatgen.ListSavedResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	limit, err := parseIntParam(req.GetLimit(), "limit", 50, 1, 100)
	if err != nil {
		return nil, err
	}
	page, err := a.App.SavedItems(ctx, user.ID, req.GetCursor(), int(limit))
	if err != nil {
		return nil, err
	}
	items := make([]*chatgen.SavedItemDTO, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, &chatgen.SavedItemDTO{
			Message:       toMessageDTO(it.Message),
			Conversation:  toAttentionConversationDTO(it.Conversation),
			WorkspaceName: it.WorkspaceName,
			SavedAt:       it.SavedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return &chatgen.ListSavedResp{
		Items:  items,
		Cursor: optString(page.Cursor),
	}, nil
}

func (a *Adapter) ListConversationSaved(ctx context.Context, req *chatgen.ListConversationSavedReq) (*chatgen.ListConversationSavedResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	ids, err := a.App.SavedMessageIDs(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []string{}
	}
	return &chatgen.ListConversationSavedResp{MessageIds: ids}, nil
}

func (a *Adapter) MarkUnread(ctx context.Context, req *chatgen.MarkUnreadReq) (*chatgen.MarkUnreadResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	seq, err := a.App.MarkUnread(ctx, user.ID, req.ID, req.Seq)
	if err != nil {
		return nil, err
	}
	return &chatgen.MarkUnreadResp{
		ConversationId:  req.ID,
		ManualUnreadSeq: seq,
	}, nil
}

func (a *Adapter) ClearUnread(ctx context.Context, req *chatgen.ClearUnreadReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.ClearUnread(ctx, user.ID, req.ID); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}
