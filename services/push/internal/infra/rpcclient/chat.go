package rpcclient

import (
	"context"
	"fmt"
	"time"

	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/chatservice"
	"teamie/pkg/rpcx"
	"teamie/services/push/internal/domain"
)

// Chat wraps the Kitex ChatService client for push recipient authorization and readiness checks.
type Chat struct {
	Client       chatservice.Client
	ServiceToken string
}

// NewChatClient dials the Chat RPC service at addr.
func NewChatClient(addr, serviceToken string) (*Chat, error) {
	c, err := chatservice.NewClient("teamie.chat", rpcx.ClientOptions(addr, 3*time.Second, time.Second)...)
	return &Chat{Client: c, ServiceToken: serviceToken}, err
}

func (c *Chat) EvaluatePushRecipients(ctx context.Context, conversationID, messageID string, candidateUserIDs []string) (domain.PushEvaluationResult, error) {
	r, err := c.Client.EvaluatePushRecipients(ctx, &im.PushEvaluateRequest{
		ServiceToken:     c.ServiceToken,
		ConversationId:   conversationID,
		MessageId:        messageID,
		CandidateUserIds: candidateUserIDs,
	})
	if err != nil || r == nil {
		return domain.PushEvaluationResult{}, fmt.Errorf("chat evaluate push recipients failed")
	}
	if !r.Ok {
		code := r.ErrorCode
		if code == "" {
			code = "evaluate_failed"
		}
		return domain.PushEvaluationResult{}, fmt.Errorf("chat evaluate push recipients rejected: %s", code)
	}
	ids := r.RecipientUserIds
	if ids == nil {
		ids = []string{}
	}
	return domain.PushEvaluationResult{
		WorkspaceID:      r.WorkspaceId,
		ConversationID:   r.ConversationId,
		MessageID:        r.MessageId,
		RecipientUserIDs: ids,
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
