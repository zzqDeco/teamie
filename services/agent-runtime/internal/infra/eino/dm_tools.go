package eino

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const directMessageGuidance = `
Direct messages and workspace member lookup
To proactively start or resume a 1:1 direct message with a user in an authorized workspace:
1. Identify the workspaceId (from the source message envelope, sourceRefs, or im_list_conversations) and the target recipient's userId. If you only have a display name or need to verify current workspace membership, call im_list_workspace_members with that workspaceId to resolve the exact userId. Never guess or invent a userId.
2. Call im_open_direct_conversation with the workspaceId and recipient userId to create or reopen the 1:1 direct conversation and obtain its conversationId.
3. Call im_send_message (and im_upload_file first if delivering a file) using that direct conversationId. Opening a direct conversation alone does not send a message.
Respect conversation and audience boundaries: only share content in a direct message that the recipient is authorized to receive, and never disclose unrelated private conversations.
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

func (t *Tools) BuildDirectMessageTools() ([]tool.BaseTool, error) {
	listMembersTool, err := utils.InferTool("im_list_workspace_members", "List current members (userId, displayName, role, kind) of an authorized workspace. Use this to resolve a participant's exact userId before opening a direct conversation or mentioning them.", func(ctx context.Context, args *listWorkspaceMembersArgs) (*Observation, error) {
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
		return t.execute(ctx, "im_open_direct_conversation", args, func(ctx context.Context, _ string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "im.dm.create") {
				return &Observation{Error: "feature_disabled"}, nil
			}
			agentID := t.AgentID
			if agentID == "" && t.Lease != nil {
				agentID = t.Lease.AgentID
			}
			if agentID == "" {
				agentID = run.AgentID
			}
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
			if err := t.IM.Call(ctx, http.MethodPost, "/api/v1/workspaces/"+url.PathEscape(args.WorkspaceID)+"/conversations", body, &out); err != nil {
				return deliveryFailure(err)
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

	return []tool.BaseTool{listMembersTool, openDirectTool}, nil
}
