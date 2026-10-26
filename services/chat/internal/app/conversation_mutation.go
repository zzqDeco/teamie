package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"teamie/services/chat/internal/domain"
)

// The request key is caller-scoped, shared by browser and delegated principals.
// A receipt and its side effect commit in the same Chat-owned transaction.
func (s *Service) conversationMutation(ctx context.Context, actor, key string, payload any, apply func(context.Context) (domain.Conversation, error), replay func(context.Context, string) (domain.Conversation, error)) (domain.Conversation, error) {
	if key == "" {
		return apply(ctx)
	}
	if !domain.ValidUploadKey(key) {
		return domain.Conversation{}, domain.Err(400, "invalid_idempotency_key", "Invalid idempotency key")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.Conversation{}, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var result domain.Conversation
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		receipt, found, err := s.Mutations.LockMutation(txCtx, actor, key)
		if err != nil {
			return err
		}
		if found {
			if receipt.RequestHash != hash {
				return domain.Err(409, "idempotency_conflict", "Request key was used for another operation")
			}
			result, err = replay(txCtx, receipt.ConversationID)
			return err
		}
		result, err = apply(txCtx)
		if err != nil {
			return err
		}
		return s.Mutations.SaveMutation(txCtx, actor, key, domain.ConversationMutation{RequestHash: hash, ConversationID: result.ID})
	})
	return result, err
}
func (s *Service) CreateConversationWithKey(ctx context.Context, actor, workspace, kind, name string, members []string, key string) (domain.Conversation, error) {
	canonical := append([]string{actor}, members...)
	slices.Sort(canonical)
	canonical = slices.Compact(canonical)
	payload := struct {
		Operation, Workspace, Kind, Name string
		Members                          []string
	}{"create_conversation", workspace, kind, strings.TrimSpace(name), canonical}
	return s.conversationMutation(ctx, actor, key, payload, func(c context.Context) (domain.Conversation, error) {
		return s.CreateConversation(c, actor, workspace, kind, name, members)
	}, func(c context.Context, id string) (domain.Conversation, error) {
		return s.Conversations.LockConversation(c, id, actor)
	})
}
func (s *Service) AddConversationMemberWithKey(ctx context.Context, actor, conversation, target, key string) (domain.Conversation, error) {
	payload := struct{ Operation, Conversation, Target string }{"add_member", conversation, target}
	return s.conversationMutation(ctx, actor, key, payload, func(c context.Context) (domain.Conversation, error) {
		return s.GroupMember(c, actor, conversation, target, true)
	}, func(c context.Context, _ string) (domain.Conversation, error) {
		return s.groupMember(c, actor, conversation, target, true, true)
	})
}
