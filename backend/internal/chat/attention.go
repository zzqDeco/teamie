package chat

import (
	"context"
)

type AttentionItem struct {
	Message       Message               `json:"message"`
	Conversation  AttentionConversation `json:"conversation"`
	WorkspaceName string                `json:"workspaceName"`
	Reasons       []string              `json:"reasons"`
	IsRead        bool                  `json:"isRead"`
}

type AttentionPage struct {
	Items              []AttentionItem `json:"items"`
	Cursor             string          `json:"cursor,omitempty"`
	UnreadCount        int64           `json:"unreadCount"`
	ChannelUnreadCount int64           `json:"channelUnreadCount"`
}

func (s *Store) Attention(ctx context.Context, user, rawCursor string, limit int, unreadOnly bool) (AttentionPage, error) {
	page, err := s.App(nil, "").Attention(ctx, user, rawCursor, limit, unreadOnly)
	if err != nil {
		return AttentionPage{Items: []AttentionItem{}}, compatErr(err)
	}
	items := make([]AttentionItem, len(page.Items))
	for i, it := range page.Items {
		items[i] = AttentionItem{
			Message:       fromDomainMessage(it.Message),
			Conversation:  it.Conversation,
			WorkspaceName: it.WorkspaceName,
			Reasons:       it.Reasons,
			IsRead:        it.IsRead,
		}
	}
	return AttentionPage{
		Items:              items,
		Cursor:             page.Cursor,
		UnreadCount:        page.UnreadCount,
		ChannelUnreadCount: page.ChannelUnreadCount,
	}, nil
}
