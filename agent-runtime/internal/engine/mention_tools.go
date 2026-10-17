package engine

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"teamie/backend/pkg/imlimits"
	"teamie/backend/pkg/imtext"
)

const mentionGuidance = `
Structured mentions
The message envelope's mentions array identifies addressed recipients by userId and UTF-16 text ranges. Names and plain @ text alone never establish identity. Mentions express who is addressed; they do not grant membership, authority or permission to disclose other conversations. All new messages still reach your continuous Session. In groups, use the structured recipients and reply source to decide whether your contribution is needed; ordinary chatter and acknowledgments need no reply. To mention someone, resolve their current userId through normal conversation membership and explicitly call im_send_message with a matching @label text range. Keep the source request/replyToId when delivering work or files and during follow-up questions.
`

func mentionsForRun(run *Run) bool {
	if run == nil || contextProfile(run) != contextProfileV1 || run.toolProfile == v034ToolProfile {
		return false
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	return err == nil && budget.StructuredMentions
}

type mentionedSendArgs struct {
	ConversationID string           `json:"conversationId" jsonschema:"required,description=Verified destination conversation"`
	Body           string           `json:"body,omitempty" jsonschema:"description=Public message text or caption; @label ranges refer to this exact text"`
	ReplyToID      string           `json:"replyToId,omitempty" jsonschema:"description=Source message in this same conversation"`
	AttachmentIDs  []string         `json:"attachmentIds,omitempty" jsonschema:"description=Up to 8 IDs from im_upload_file in this conversation"`
	Mentions       []imtext.Mention `json:"mentions,omitempty" jsonschema:"description=Up to 32 ordered nonoverlapping @label ranges; userId is a current conversation member; start and end are half-open UTF-16 offsets, not byte offsets"`
}

type mentionedTextArgs struct {
	ConversationID string           `json:"conversationId" jsonschema:"required"`
	Body           string           `json:"body" jsonschema:"required,description=Public message text; @label ranges refer to this exact text"`
	ReplyToID      string           `json:"replyToId,omitempty"`
	Mentions       []imtext.Mention `json:"mentions,omitempty" jsonschema:"description=Up to 32 ordered nonoverlapping @label ranges with current member userId and half-open UTF-16 start/end offsets"`
}

func (t *Tools) BuildIMWithMentions(files bool) ([]tool.BaseTool, error) {
	items, err := t.buildIM(files)
	if err != nil {
		return nil, err
	}
	const description = "The ONLY way to speak, reply or mention participants in IM. Use the verified source conversation and optional replyToId. Structured mentions contain real current member userIds and matching @label UTF-16 text ranges; plain @ text conveys no identity. Respect the audience and never repeat a confirmed send. Ordinary final text stays private."
	if files {
		items[3], err = utils.InferTool("im_send_message", description+" Files use attachmentIds from im_upload_file; file-only messages are valid.", t.sendMentioned, utils.WithMarshalOutput(marshalObservation))
	} else {
		items[3], err = utils.InferTool("im_send_message", description, func(ctx context.Context, args *mentionedTextArgs) (*Observation, error) {
			return t.sendMentioned(ctx, &mentionedSendArgs{ConversationID: args.ConversationID, Body: args.Body, ReplyToID: args.ReplyToID, Mentions: args.Mentions})
		}, utils.WithMarshalOutput(marshalObservation))
	}
	return items, err
}

func (t *Tools) sendMentioned(ctx context.Context, args *mentionedSendArgs) (*Observation, error) {
	run, err := CurrentRun(ctx)
	if err != nil {
		return nil, err
	}
	if !mentionsForRun(run) {
		return nil, errors.New("mentions_not_admitted")
	}
	return t.execute(ctx, "im_send_message", args, func(ctx context.Context, id string) (*Observation, error) {
		if !validUUID(args.ConversationID) || args.ReplyToID != "" && !validUUID(args.ReplyToID) || len([]rune(args.Body)) > 8000 || strings.TrimSpace(args.Body) == "" && len(args.AttachmentIDs) == 0 || imtext.ValidateMentions(args.Body, args.Mentions) != nil || len(args.AttachmentIDs) > imlimits.AttachmentsPerMessage || len(args.AttachmentIDs) > 0 && !fileDeliveryForRun(run) {
			return &Observation{Error: "invalid_arguments"}, nil
		}
		seen := map[string]bool{}
		for _, attachment := range args.AttachmentIDs {
			if !validUUID(attachment) || seen[attachment] {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			seen[attachment] = true
		}
		body := map[string]any{"clientMessageId": id, "body": args.Body}
		if args.ReplyToID != "" {
			body["replyToId"] = args.ReplyToID
		}
		if len(args.AttachmentIDs) > 0 {
			body["attachmentIds"] = args.AttachmentIDs
		}
		if len(args.Mentions) > 0 {
			body["mentions"] = args.Mentions
		}
		var response struct {
			Message Message `json:"message"`
		}
		if err := t.IM.Call(ctx, http.MethodPost, "/api/v1/conversations/"+args.ConversationID+"/messages", body, &response); err != nil {
			return deliveryFailure(err)
		}
		return &Observation{OK: true, Data: response.Message}, nil
	})
}
