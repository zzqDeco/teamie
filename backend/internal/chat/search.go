package chat

import (
	"context"
)

type WorkspaceSearchItem struct {
	Message      Message               `json:"message"`
	Conversation AttentionConversation `json:"conversation"`
}

type WorkspaceSearchPage struct {
	Items  []WorkspaceSearchItem `json:"items"`
	Cursor string                `json:"cursor,omitempty"`
}

func (s *Store) SearchWorkspaceMessages(ctx context.Context, user, workspaceID, query, rawCursor string, limit int) (WorkspaceSearchPage, error) {
	page, err := s.App(nil, "").SearchWorkspaceMessages(ctx, user, workspaceID, query, rawCursor, limit)
	if err != nil {
		return WorkspaceSearchPage{Items: []WorkspaceSearchItem{}}, compatErr(err)
	}
	items := make([]WorkspaceSearchItem, len(page.Items))
	for i, it := range page.Items {
		items[i] = WorkspaceSearchItem{
			Message:      fromDomainMessage(it.Message),
			Conversation: it.Conversation,
		}
	}
	return WorkspaceSearchPage{
		Items:  items,
		Cursor: page.Cursor,
	}, nil
}
