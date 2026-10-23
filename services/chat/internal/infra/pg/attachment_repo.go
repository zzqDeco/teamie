package pg

import (
	"context"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

func (r *Repository) FindAttachmentUpload(ctx context.Context, userID, key string) (domain.AttachmentUpload, bool, error) {
	var out domain.AttachmentUpload
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT u.conversation_id,u.fingerprint,COALESCE(a.id,''),COALESCE(a.filename,''),COALESCE(a.media_type,''),COALESCE(a.size_bytes,0),COALESCE(a.sha256,''),
 a.id IS NOT NULL AND ((a.message_id IS NULL AND a.expires_at>now()) OR (m.id IS NOT NULL AND m.retracted_at IS NULL))
 FROM attachment_uploads u LEFT JOIN attachments a ON a.id=u.attachment_id LEFT JOIN messages m ON m.id=a.message_id
 WHERE u.uploader_id=$1 AND u.upload_key=$2`, userID, key).Scan(&out.Conversation, &out.Fingerprint, &out.Attachment.ID, &out.Attachment.Filename, &out.Attachment.MediaType, &out.Attachment.SizeBytes, &out.Attachment.SHA256, &out.Available)
	if postgres.IsNoRows(err) {
		return domain.AttachmentUpload{}, false, nil
	}
	if err != nil {
		return domain.AttachmentUpload{}, false, err
	}
	return out, true, nil
}

func (r *Repository) DeleteExpiredPendingForSender(ctx context.Context, userID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM attachments WHERE uploader_id=$1 AND message_id IS NULL AND expires_at<=now()", userID)
	return err
}

func (r *Repository) CountPendingForSender(ctx context.Context, userID string) (int, error) {
	var pending int
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT count(*) FROM attachments WHERE uploader_id=$1 AND message_id IS NULL", userID).Scan(&pending)
	return pending, err
}

func (r *Repository) InsertAttachment(ctx context.Context, a domain.Attachment, conversationID, uploaderID string, content []byte) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "INSERT INTO attachments(id,conversation_id,uploader_id,filename,media_type,size_bytes,sha256,content) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", a.ID, conversationID, uploaderID, a.Filename, a.MediaType, a.SizeBytes, a.SHA256, content)
	return err
}

func (r *Repository) InsertAttachmentUpload(ctx context.Context, uploaderID, key, conversationID, fingerprint, attachmentID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "INSERT INTO attachment_uploads(uploader_id,upload_key,conversation_id,fingerprint,attachment_id) VALUES($1,$2,$3,$4,$5)", uploaderID, key, conversationID, fingerprint, attachmentID)
	return err
}

func (r *Repository) DeleteAllExpiredPending(ctx context.Context) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM attachments WHERE message_id IS NULL AND expires_at<=now()")
	return err
}

func (r *Repository) LockAttachmentForUpdate(ctx context.Context, attachmentID, conversationID string) (string, *string, bool, error) {
	var owner string
	var message *string
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT uploader_id,message_id FROM attachments WHERE id=$1 AND conversation_id=$2 FOR UPDATE", attachmentID, conversationID).Scan(&owner, &message)
	if postgres.IsNoRows(err) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return owner, message, true, nil
}

func (r *Repository) DeleteAttachmentByID(ctx context.Context, attachmentID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM attachments WHERE id=$1", attachmentID)
	return err
}

func (r *Repository) AttachFilesToMessage(ctx context.Context, userID, conversationID, messageID string, attachmentIDs []string) ([]domain.Attachment, error) {
	conn := r.DB.Conn(ctx)
	out := []domain.Attachment{}
	for _, id := range attachmentIDs {
		var a domain.Attachment
		err := conn.QueryRow(ctx, `UPDATE attachments SET message_id=$1 WHERE id=$2 AND conversation_id=$3 AND uploader_id=$4 AND message_id IS NULL AND expires_at>now() RETURNING id,filename,media_type,size_bytes,sha256`, messageID, id, conversationID, userID).Scan(&a.ID, &a.Filename, &a.MediaType, &a.SizeBytes, &a.SHA256)
		if postgres.IsNoRows(err) {
			return nil, domain.ErrInvalidAttachmentUnavailable()
		}
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *Repository) GetAuthorizedAttachmentContent(ctx context.Context, attachmentID, conversationID, userID string) (domain.Attachment, []byte, error) {
	var a domain.Attachment
	var content []byte
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT a.id,a.filename,a.media_type,a.size_bytes,a.sha256,a.content FROM attachments a LEFT JOIN messages m ON m.id=a.message_id WHERE a.id=$1 AND a.conversation_id=$2 AND ((a.message_id IS NULL AND a.uploader_id=$3 AND a.expires_at>now()) OR (a.message_id IS NOT NULL AND m.retracted_at IS NULL))`, attachmentID, conversationID, userID).Scan(&a.ID, &a.Filename, &a.MediaType, &a.SizeBytes, &a.SHA256, &content)
	return a, content, notFound(err)
}
