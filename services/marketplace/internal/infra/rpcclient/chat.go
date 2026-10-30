package rpcclient

import (
	"context"
	"fmt"
	"time"

	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/chatservice"
	"teamie/pkg/httpx"
	"teamie/pkg/rpcx"
	"teamie/services/marketplace/internal/domain"
)

// Chat wraps the Kitex ChatService client for read-only workspace role and conversation scope checks.
type Chat struct {
	Client chatservice.Client
}

// NewChatClient dials the Chat RPC service at addr.
func NewChatClient(addr string) (*Chat, error) {
	c, err := chatservice.NewClient("teamie.chat", rpcx.ClientOptions(addr, 3*time.Second, time.Second)...)
	return &Chat{Client: c}, err
}

func (c *Chat) WorkspaceRole(ctx context.Context, workspaceID, userID string) (domain.WorkspaceScope, error) {
	r, err := c.Client.GetWorkspaceRole(ctx, &im.WorkspaceAccessRequest{
		WorkspaceId: workspaceID,
		UserId:      userID,
	})
	if err != nil || r == nil {
		return domain.WorkspaceScope{}, httpx.Err(503, "chat_unavailable", "Chat service is temporarily unavailable")
	}
	return domain.WorkspaceScope{
		Allowed: r.Allowed,
		OwnerID: r.OwnerId,
		Role:    r.Role,
	}, nil
}

func (c *Chat) ConversationScope(ctx context.Context, conversationID, userID string) (domain.ConversationScope, error) {
	r, err := c.Client.GetConversationScope(ctx, &im.AccessRequest{
		ConversationId: conversationID,
		UserId:         userID,
	})
	if err != nil || r == nil {
		return domain.ConversationScope{}, httpx.Err(503, "chat_unavailable", "Chat service is temporarily unavailable")
	}
	return domain.ConversationScope{
		Allowed:          r.Allowed,
		WorkspaceID:      r.WorkspaceId,
		WorkspaceOwnerID: r.WorkspaceOwnerId,
		ConversationType: r.ConversationType,
	}, nil
}

func (c *Chat) Ping(ctx context.Context) error {
	r, err := c.Client.Ping(ctx, &im.PingRequest{})
	if err != nil {
		return err
	}
	if !r.Ready {
		return fmt.Errorf("chat not ready")
	}
	return nil
}
