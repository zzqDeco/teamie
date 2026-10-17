package chat

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"teamie/backend/internal/api"
)

func validUploadKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

type attachmentUpload struct {
	conversation, fingerprint string
	attachment                Attachment
	available                 bool
}

func findAttachmentUpload(ctx context.Context, q querier, user, key string) (attachmentUpload, error) {
	var out attachmentUpload
	err := q.QueryRow(ctx, `SELECT u.conversation_id,u.fingerprint,COALESCE(a.id,''),COALESCE(a.filename,''),COALESCE(a.media_type,''),COALESCE(a.size_bytes,0),COALESCE(a.sha256,''),
 a.id IS NOT NULL AND ((a.message_id IS NULL AND a.expires_at>now()) OR (m.id IS NOT NULL AND m.retracted_at IS NULL))
 FROM attachment_uploads u LEFT JOIN attachments a ON a.id=u.attachment_id LEFT JOIN messages m ON m.id=a.message_id
 WHERE u.uploader_id=$1 AND u.upload_key=$2`, user, key).Scan(&out.conversation, &out.fingerprint, &out.attachment.ID, &out.attachment.Filename, &out.attachment.MediaType, &out.attachment.SizeBytes, &out.attachment.SHA256, &out.available)
	return out, err
}

// Metadata-only discovery recovers a lost POST response without re-reading a
// mutable source file. The current uploader and conversation ACL are required,
// including after linkage, and a deleted upload key is never made reusable.
func (s *Store) LookupAttachmentUpload(ctx context.Context, user, conversation, key string) (Attachment, error) {
	if !validUploadKey(key) {
		return Attachment{}, api.Err(400, "invalid_upload_key", "Invalid upload key")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Attachment{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockConversation(ctx, tx, conversation, user); err != nil {
		return Attachment{}, err
	}
	old, err := findAttachmentUpload(ctx, tx, user, key)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && old.conversation != conversation {
		return Attachment{}, api.Err(404, "attachment_upload_not_found", "No upload with this key")
	}
	if err != nil {
		return Attachment{}, err
	}
	if !old.available {
		return Attachment{}, api.Err(410, "attachment_upload_unavailable", "This upload was removed or expired")
	}
	return old.attachment, tx.Commit(ctx)
}
