package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/jackc/pgx/v5"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/backend/pkg/imlimits"
)

const fileDeliveryGuidance = `
File results
After checking that the requested work and its output file are complete, use im_upload_file to upload that specific workspace file to its verified source/destination conversation. Uploading alone does not deliver a message: use im_send_message with the returned attachmentIds and an optional concise caption. Keep filenames useful to the recipient; never put workspace paths, internal task/call IDs or private execution details in a public caption. A file-only message is valid. Keep the real task origin and audience through background completion and context compression. Respect current source access, upload errors and confirmed receipts; never repeat a completed delivery or choose another conversation because the intended one is unavailable.
`

func fileDeliveryForRun(run *Run) bool {
	if run == nil || contextProfile(run) != contextProfileV1 || run.toolProfile == v034ToolProfile {
		return false
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	return err == nil && budget.FileDelivery
}

func toolRequestID(run *Run, call string) string {
	return "tool-" + digest([]byte(run.AgentID+"/"+run.ID+"/"+call))
}

type uploadFileArgs struct {
	ConversationID string `json:"conversationId" jsonschema:"required,description=Verified destination conversation for this source request"`
	Path           string `json:"path" jsonschema:"required,description=Completed file in your own Sandbox filesystem; no URL or host path"`
	Filename       string `json:"filename,omitempty" jsonschema:"description=Recipient-visible filename only; defaults to the path basename; no directories or control characters"`
}

type fileUploadObservation struct {
	ConversationID string     `json:"conversationId"`
	Attachment     Attachment `json:"attachment"`
}

func validFileDescriptor(a Attachment) bool {
	decoded, err := hex.DecodeString(a.SHA256)
	return validUUID(a.ID) && imlimits.ValidFilename(a.Filename) && a.SizeBytes > 0 && a.SizeBytes <= imlimits.AttachmentBytes && err == nil && len(decoded) == sha256.Size
}

func (imc *IM) uploadedFile(ctx context.Context, conversation, key string) (*Attachment, error) {
	var out struct {
		Attachment Attachment `json:"attachment"`
	}
	err := imc.Call(ctx, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(conversation)+"/attachment-uploads/"+url.PathEscape(key), nil, &out)
	if err != nil {
		return nil, err
	}
	if !validFileDescriptor(out.Attachment) {
		return nil, errors.New("invalid_attachment_descriptor")
	}
	return &out.Attachment, nil
}

func (imc *IM) uploadFile(ctx context.Context, conversation, key, filename string, content []byte) (*Attachment, error) {
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(content); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	var out struct {
		Attachment Attachment `json:"attachment"`
	}
	err = imc.request(ctx, imc.BaseURL, http.MethodPost, "/api/v1/conversations/"+url.PathEscape(conversation)+"/attachments", &data, writer.FormDataContentType(), map[string]string{"Idempotency-Key": key}, &out)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	if !validFileDescriptor(out.Attachment) || out.Attachment.Filename != filename || out.Attachment.SizeBytes != len(content) || out.Attachment.SHA256 != hex.EncodeToString(sum[:]) {
		return nil, errors.New("attachment_integrity")
	}
	return &out.Attachment, nil
}

func deliveryFailure(err error) (*Observation, error) {
	var remote *APIError
	if errors.As(err, &remote) && remote.Status < 500 && remote.Status != 429 && remote.Status != 401 {
		return observeError(err), nil
	}
	return nil, &ModelError{Code: "im_unavailable", Retry: true}
}

func (t *Tools) uploadFileTool() (tool.BaseTool, error) {
	return utils.InferTool("im_upload_file", "Explicitly upload one completed Sandbox file (1 byte to 4 MiB) to a verified source/destination conversation. Returns only its conversationId and normal attachment descriptor; never bytes, base64 or a host URL. This does not post a message: deliver the returned ID using im_send_message attachmentIds. Reuses a committed upload on retry even if the workspace file changed or was removed. Respect source privacy and current membership; never upload unrelated private files.", func(ctx context.Context, args *uploadFileArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !fileDeliveryForRun(run) {
			return nil, errors.New("file_delivery_not_admitted")
		}
		return t.execute(ctx, "im_upload_file", args, func(ctx context.Context, key string) (*Observation, error) {
			filename := args.Filename
			if filename == "" {
				filename = path.Base(args.Path)
			}
			if !validUUID(args.ConversationID) || args.Path == "" || len(args.Path) > 4096 || !imlimits.ValidFilename(filename) {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			existing, err := t.IM.uploadedFile(ctx, args.ConversationID, key)
			if err == nil {
				if existing.Filename != filename {
					return nil, ErrReplayConflict
				}
				return &Observation{OK: true, Data: fileUploadObservation{args.ConversationID, *existing}}, nil
			}
			var remote *APIError
			if !errors.As(err, &remote) || remote.Status != 404 || remote.Code != "attachment_upload_not_found" {
				return deliveryFailure(err)
			}
			if t.Sandbox == nil {
				return nil, &ModelError{Code: "sandbox_unavailable", Retry: true}
			}
			out, err := t.Sandbox.ExportFile(ctx, &wire.SandboxExportRequest{ServiceToken: t.SandboxToken, AgentId: run.AgentID, Path: args.Path})
			if err != nil || out == nil {
				return nil, &ModelError{Code: "sandbox_unavailable", Retry: true}
			}
			if !out.Ok {
				return &Observation{Error: out.GetErrorCode()}, nil
			}
			sum := sha256.Sum256(out.Content)
			if len(out.Content) < 1 || len(out.Content) > imlimits.AttachmentBytes || out.GetSha256() != hex.EncodeToString(sum[:]) {
				return nil, errors.New("attachment_integrity")
			}
			if t.Authorize != nil {
				if err = t.Authorize(ctx); err != nil {
					return nil, err
				}
			}
			if err = t.Store.CheckLease(ctx, t.Lease); err != nil {
				return nil, err
			}
			archive := &ContextArchive{Store: t.Store, Lease: t.Lease, IM: t.IM, Authorize: t.Authorize}
			payload, _ := json.Marshal(args)
			if err = archive.captureWorkspaceSources(ctx, run, compose.GetToolCallID(ctx), "im_upload_file", payload); err != nil {
				return nil, err
			}
			uploaded, err := t.IM.uploadFile(ctx, args.ConversationID, key, filename, out.Content)
			if err != nil {
				return deliveryFailure(err)
			}
			return &Observation{OK: true, Data: fileUploadObservation{args.ConversationID, *uploaded}}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
}

type fileMessageArgs struct {
	ConversationID string   `json:"conversationId" jsonschema:"required,description=Explicit destination conversation; attachments must have been uploaded here"`
	Body           string   `json:"body,omitempty" jsonschema:"description=Recipient-facing text or caption; can be empty when attachmentIds is nonempty"`
	ReplyToID      string   `json:"replyToId,omitempty" jsonschema:"description=Source message in this same conversation"`
	AttachmentIDs  []string `json:"attachmentIds,omitempty" jsonschema:"description=Up to 8 attachment IDs returned by im_upload_file for this destination; no paths or URLs"`
}

func (t *Tools) fileMessageTool() (tool.InvokableTool, error) {
	return utils.InferTool("im_send_message", "The ONLY way to speak, reply or deliver files in IM. Set the verified conversationId and optional source replyToId. Use attachmentIds from im_upload_file for this same conversation; a file-only message is valid. Only publish source-authorized results for this audience. Never include Sandbox paths, private runtime details or repeat a confirmed send. Ordinary assistant text is private.", func(ctx context.Context, args *fileMessageArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !fileDeliveryForRun(run) {
			return nil, errors.New("file_delivery_not_admitted")
		}
		return t.execute(ctx, "im_send_message", args, func(ctx context.Context, id string) (*Observation, error) {
			if !validUUID(args.ConversationID) || args.ReplyToID != "" && !validUUID(args.ReplyToID) || len([]rune(args.Body)) > 8000 || len(args.AttachmentIDs) > imlimits.AttachmentsPerMessage || strings.TrimSpace(args.Body) == "" && len(args.AttachmentIDs) == 0 {
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
			var response struct {
				Message Message `json:"message"`
			}
			if err := t.IM.Call(ctx, http.MethodPost, "/api/v1/conversations/"+args.ConversationID+"/messages", body, &response); err != nil {
				return deliveryFailure(err)
			}
			return &Observation{OK: true, Data: response.Message}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
}

// The upload is an authenticated workspace observation. Its proven source
// closure lives outside model text and is checked again on archive/cache reads.
func (a *ContextArchive) uploadedFileSources(ctx context.Context, runID, call string) ([]InputSource, error) {
	var agentID, hash string
	var raw []byte
	err := a.Store.DB.QueryRow(ctx, `SELECT r.agent_id,t.request_hash,o.response FROM runtime_tool_calls t JOIN runtime_runs r ON r.id=t.run_id JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key=$3 WHERE t.run_id=$1 AND t.call_id=$2 AND t.name='im_upload_file'`, runID, call, workspaceSourcesKey).Scan(&agentID, &hash, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errContextUnavailable
	}
	if err != nil {
		return nil, err
	}
	var snapshot workspaceSourceSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != 1 || snapshot.Name != "im_upload_file" || snapshot.RequestHash != hash {
		return nil, ErrReplayConflict
	}
	refs, err := a.workspaceSnapshotSources(ctx, agentID, snapshot.Ref)
	if err != nil {
		return nil, err
	}
	args, err := a.imArguments(ctx, runID, call, "im_upload_file")
	if err != nil {
		return nil, err
	}
	var input uploadFileArgs
	if json.Unmarshal(args, &input) != nil || !validUUID(input.ConversationID) {
		return nil, errContextUnavailable
	}
	return append(refs, InputSource{ConversationID: input.ConversationID}), nil
}
