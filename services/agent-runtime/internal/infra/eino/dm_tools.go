package eino

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const directMessageGuidance = `
Direct messages, group creation/member invitations, message retraction, and workspace member lookup
To proactively start or resume a 1:1 direct message with a user in an authorized workspace:
1. Identify the workspaceId (from the source message envelope, sourceRefs, or im_list_conversations) and the target recipient's userId. If you only have a display name or need to verify current workspace membership, call im_list_workspace_members with that workspaceId to resolve the exact userId. Never guess or invent a userId.
2. Call im_open_direct_conversation with the workspaceId and recipient userId to create or reopen the 1:1 direct conversation and obtain its conversationId.
3. Call im_send_message (and im_upload_file first if delivering a file) using that direct conversationId. Opening a direct conversation alone does not send a message.
To create a multi-member group conversation or invite additional workspace members into a group you created:
- Call im_create_group_conversation with workspaceId, optional name, and verified memberIds (resolved via im_list_workspace_members) to create a new group conversation; then call im_send_message with the returned conversationId if you wish to post an opening message.
- Call im_add_conversation_members with conversationId and verified memberIds to invite additional workspace members (humans or other Agents) into an existing group conversation.
To retract a message you authored:
- Call im_retract_message with conversationId and messageId to retract your own message if you sent it by mistake or decide to withdraw it.
Respect conversation and audience boundaries: only share content in a direct or group conversation that all recipients are authorized to receive, and never disclose unrelated private conversations.
`

func directMessagesForRun(run *Run) bool {
	if run == nil || contextProfile(run) != contextProfileV1 || run.toolProfile == v034ToolProfile {
		return false
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	return err == nil && budget.DirectMessages
}

// WorkspaceMember represents a current participant in an authorized workspace.
type WorkspaceMember struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Kind        string `json:"kind"`
}

type listWorkspaceMembersArgs struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"required,description=Authorized workspace ID from the source message or im_list_conversations"`
}

type openDirectConversationArgs struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"required,description=Authorized workspace ID shared with the recipient"`
	UserID      string `json:"userId" jsonschema:"required,description=Verified target member userId in this workspace (from source senderId, mentions, or im_list_workspace_members)"`
}

type createGroupConversationArgs struct {
	WorkspaceID string   `json:"workspaceId" jsonschema:"required,description=Authorized workspace ID where the group conversation will be created"`
	Name        string   `json:"name,omitempty" jsonschema:"description=Optional group display name (up to 80 characters)"`
	MemberIDs   []string `json:"memberIds" jsonschema:"required,description=Verified initial member userIds in this workspace (1 to 49 other members; you are included automatically)"`
}

type addConversationMembersArgs struct {
	ConversationID string   `json:"conversationId" jsonschema:"required,description=Existing group conversationId that you created or manage"`
	MemberIDs      []string `json:"memberIds,omitempty" jsonschema:"description=Verified workspace member userIds to add to this group conversation (1 to 49 userIds)"`
	UserID         string   `json:"userId,omitempty" jsonschema:"description=Optional single verified workspace member userId to add if memberIds is omitted"`
}

type retractMessageArgs struct {
	ConversationID string `json:"conversationId" jsonschema:"required,description=Conversation ID containing the message you authored"`
	MessageID      string `json:"messageId" jsonschema:"required,description=Message ID of the message you authored and want to retract"`
}

func (t *Tools) resolveAgentID(run *Run) string {
	agentID := t.AgentID
	if agentID == "" && t.Lease != nil {
		agentID = t.Lease.AgentID
	}
	if agentID == "" && run != nil {
		agentID = run.AgentID
	}
	return agentID
}

func (t *Tools) BuildDirectMessageTools() ([]tool.BaseTool, error) {
	listMembersTool, err := utils.InferTool("im_list_workspace_members", "List current members (userId, displayName, role, kind) of an authorized workspace. Use this to resolve a participant's exact userId before opening a direct conversation, creating a group, or mentioning them.", func(ctx context.Context, args *listWorkspaceMembersArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !directMessagesForRun(run) {
			return nil, errors.New("direct_messages_not_admitted")
		}
		return t.execute(ctx, "im_list_workspace_members", args, func(ctx context.Context, _ string) (*Observation, error) {
			if args == nil || !validUUID(args.WorkspaceID) {
				return &Observation{Error: "invalid_id"}, nil
			}
			var out struct {
				Members []WorkspaceMember `json:"members"`
			}
			if err := t.IM.Call(ctx, http.MethodGet, "/api/v1/workspaces/"+url.PathEscape(args.WorkspaceID)+"/members", nil, &out); err != nil {
				return observeError(err), nil
			}
			for _, m := range out.Members {
				if !validUUID(m.UserID) {
					return nil, errors.New("invalid_member_response")
				}
			}
			if out.Members == nil {
				out.Members = []WorkspaceMember{}
			}
			return &Observation{OK: true, Data: map[string]any{"workspaceId": args.WorkspaceID, "members": out.Members}}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	openDirectTool, err := utils.InferTool("im_open_direct_conversation", "Open or idempotently reopen the 1:1 direct conversation between you and a current member of an authorized workspace. Returns the direct conversation metadata including conversationId; call im_send_message with that conversationId to send the direct message.", func(ctx context.Context, args *openDirectConversationArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !directMessagesForRun(run) {
			return nil, errors.New("direct_messages_not_admitted")
		}
		return t.execute(ctx, "im_open_direct_conversation", args, func(ctx context.Context, key string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "im.dm.create") {
				return &Observation{Error: "feature_disabled"}, nil
			}
			agentID := t.resolveAgentID(run)
			if args == nil || !validUUID(args.WorkspaceID) || !validUUID(args.UserID) || args.UserID == agentID {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			var out struct {
				Conversation Conversation `json:"conversation"`
			}
			body := map[string]any{
				"type":      "direct",
				"memberIds": []string{args.UserID},
			}
			if err := t.IM.CallIdempotent(ctx, http.MethodPost, "/api/v1/workspaces/"+url.PathEscape(args.WorkspaceID)+"/conversations", key, body, &out); err != nil {
				return deliveryFailure(err)
			}
			if !validUUID(out.Conversation.ID) || !validUUID(out.Conversation.WorkspaceID) {
				return nil, errors.New("invalid_conversation_response")
			}
			if t.presenceEnforced(run) {
				convType := out.Conversation.Type
				if convType == "" {
					convType = "direct"
				}
				t.applyEnterConversation(ctx, run, out.Conversation.WorkspaceID, out.Conversation.ID, convType)
			}
			return &Observation{OK: true, Data: out.Conversation}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	createGroupTool, err := utils.InferTool("im_create_group_conversation", "Create a new multi-member group conversation in an authorized workspace with verified workspace member userIds. Returns the created group conversation metadata including conversationId; call im_send_message with that conversationId to send a message.", func(ctx context.Context, args *createGroupConversationArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !directMessagesForRun(run) {
			return nil, errors.New("direct_messages_not_admitted")
		}
		return t.execute(ctx, "im_create_group_conversation", args, func(ctx context.Context, key string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "im.group.create") {
				return &Observation{Error: "feature_disabled"}, nil
			}
			if args == nil || !validUUID(args.WorkspaceID) {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			name := strings.TrimSpace(args.Name)
			if len([]rune(name)) > 80 {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			agentID := t.resolveAgentID(run)
			seen := map[string]bool{}
			otherMembers := make([]string, 0, len(args.MemberIDs))
			for _, id := range args.MemberIDs {
				if !validUUID(id) {
					return &Observation{Error: "invalid_arguments"}, nil
				}
				if id == agentID || seen[id] {
					continue
				}
				seen[id] = true
				otherMembers = append(otherMembers, id)
			}
			if len(otherMembers) < 1 || len(otherMembers) > 49 {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			var out struct {
				Conversation Conversation `json:"conversation"`
			}
			body := map[string]any{
				"type":      "group",
				"memberIds": otherMembers,
			}
			if name != "" {
				body["name"] = name
			}
			if err := t.IM.CallIdempotent(ctx, http.MethodPost, "/api/v1/workspaces/"+url.PathEscape(args.WorkspaceID)+"/conversations", key, body, &out); err != nil {
				return deliveryFailure(err)
			}
			if !validUUID(out.Conversation.ID) || !validUUID(out.Conversation.WorkspaceID) {
				return nil, errors.New("invalid_conversation_response")
			}
			if t.presenceEnforced(run) {
				convType := out.Conversation.Type
				if convType == "" {
					convType = "group"
				}
				t.applyEnterConversation(ctx, run, out.Conversation.WorkspaceID, out.Conversation.ID, convType)
			}
			return &Observation{OK: true, Data: out.Conversation}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	addMembersTool, err := utils.InferTool("im_add_conversation_members", "Invite and add one or more verified workspace members (humans or other Agents) into an existing group conversation that you created or manage.", func(ctx context.Context, args *addConversationMembersArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !directMessagesForRun(run) {
			return nil, errors.New("direct_messages_not_admitted")
		}
		return t.execute(ctx, "im_add_conversation_members", args, func(ctx context.Context, key string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "im.group.create") {
				return &Observation{Error: "feature_disabled"}, nil
			}
			if args == nil || !validUUID(args.ConversationID) {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			rawIDs := make([]string, 0, len(args.MemberIDs)+1)
			rawIDs = append(rawIDs, args.MemberIDs...)
			if args.UserID != "" {
				rawIDs = append(rawIDs, args.UserID)
			}
			seen := map[string]bool{}
			targets := make([]string, 0, len(rawIDs))
			for _, id := range rawIDs {
				if !validUUID(id) {
					return &Observation{Error: "invalid_arguments"}, nil
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				targets = append(targets, id)
			}
			if len(targets) < 1 || len(targets) > 49 {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			var out struct {
				Conversation Conversation `json:"conversation"`
			}
			path := "/api/v1/conversations/" + url.PathEscape(args.ConversationID) + "/members"
			for _, uid := range targets {
				if err := t.IM.CallIdempotent(ctx, http.MethodPost, path, "member-"+digest([]byte(key+"/"+uid)), map[string]any{"userId": uid}, &out); err != nil {
					return deliveryFailure(err)
				}
			}
			if !validUUID(out.Conversation.ID) || !validUUID(out.Conversation.WorkspaceID) {
				return nil, errors.New("invalid_conversation_response")
			}
			return &Observation{OK: true, Data: out.Conversation}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	retractTool, err := utils.InferTool("im_retract_message", "Retract a message authored by you in an authorized conversation. Retracted messages become tombstones with empty body and removed attachments.", func(ctx context.Context, args *retractMessageArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !directMessagesForRun(run) {
			return nil, errors.New("direct_messages_not_admitted")
		}
		return t.execute(ctx, "im_retract_message", args, func(ctx context.Context, _ string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "im.message.retract") {
				return &Observation{Error: "feature_disabled"}, nil
			}
			if args == nil || !validUUID(args.ConversationID) || !validUUID(args.MessageID) {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			var out struct {
				Message Message `json:"message"`
			}
			path := "/api/v1/conversations/" + url.PathEscape(args.ConversationID) + "/messages/" + url.PathEscape(args.MessageID)
			if err := t.IM.Call(ctx, http.MethodDelete, path, nil, &out); err != nil {
				return deliveryFailure(err)
			}
			if !validUUID(out.Message.ID) || !validUUID(out.Message.ConversationID) || out.Message.RetractedAt == nil {
				return nil, errors.New("invalid_message_response")
			}
			return &Observation{OK: true, Data: out.Message}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{listMembersTool, openDirectTool, createGroupTool, addMembersTool, retractTool}, nil
}
