package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"teamie/backend/internal/api"
	"teamie/backend/pkg/imlimits"
)

// Shared upload/download admission bounds bytea buffers under Chat's 144 MiB
// Go memory budget. Hertz streams before admission instead of buffering bodies.
var attachmentTransfers = make(chan struct{}, 2)

func acquireAttachmentTransfer() (func(), error) {
	select {
	case attachmentTransfers <- struct{}{}:
		return func() { <-attachmentTransfers }, nil
	default:
		return nil, api.Err(429, "attachment_busy", "File transfers are busy; please retry")
	}
}

func validateFilename(name string) error {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || len([]rune(name)) > 180 || len(name) > 512 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\") {
		return api.Err(400, "invalid_filename", "Use a filename of 1 to 180 characters without path separators")
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return api.Err(400, "invalid_filename", "Filename contains an unsupported control character")
		}
	}
	return nil
}

func validateAttachmentIDs(ids []string) error {
	if len(ids) > imlimits.AttachmentsPerMessage {
		return api.Err(400, "too_many_attachments", "A message supports at most 8 files")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !api.BoundedString(id, 1, 128) || seen[id] {
			return api.Err(400, "invalid_attachment", "Attachment IDs must be unique")
		}
		seen[id] = true
	}
	return nil
}

func (s *Store) UploadAttachment(ctx context.Context, user, conversation, filename string, content []byte) (Attachment, error) {
	var a Attachment
	if e := s.requireFlag("im.attachments.upload"); e != nil {
		return a, e
	}
	if e := validateFilename(filename); e != nil {
		return a, e
	}
	if len(content) == 0 || len(content) > imlimits.AttachmentBytes {
		return a, api.Err(413, "attachment_too_large", "Files must contain 1 byte to 4 MiB")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return a, e
	}
	defer tx.Rollback(ctx)
	// A sender-wide lock makes the pending quota atomic across conversations.
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,704104))", user); e != nil {
		return a, e
	}
	if _, e = lockConversation(ctx, tx, conversation, user); e != nil {
		return a, e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM attachments WHERE uploader_id=$1 AND message_id IS NULL AND expires_at<=now()", user); e != nil {
		return a, e
	}
	var pending int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM attachments WHERE uploader_id=$1 AND message_id IS NULL", user).Scan(&pending); e != nil {
		return a, e
	}
	if pending >= imlimits.PendingAttachmentsPerSender {
		return a, api.Err(429, "attachment_capacity", "Remove pending files or wait for them to expire")
	}
	h := sha256.Sum256(content)
	a = Attachment{ID: api.ID(), Filename: filename, MediaType: http.DetectContentType(content), SizeBytes: len(content), SHA256: hex.EncodeToString(h[:])}
	_, e = tx.Exec(ctx, "INSERT INTO attachments(id,conversation_id,uploader_id,filename,media_type,size_bytes,sha256,content) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", a.ID, conversation, user, a.Filename, a.MediaType, a.SizeBytes, a.SHA256, content)
	if e != nil {
		return a, e
	}
	return a, tx.Commit(ctx)
}

// Runs independently of writes, including when uploads are disabled. Linked
// files live as long as their messages; retraction erases their binary content.
func (s *Store) RunAttachmentCleanup(ctx context.Context) {
	timer := time.NewTicker(time.Hour)
	defer timer.Stop()
	for {
		cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, _ = s.DB.Exec(cleanup, "DELETE FROM attachments WHERE message_id IS NULL AND expires_at<=now()")
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (s *Store) DeletePendingAttachment(ctx context.Context, user, conversation, id string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = lockConversation(ctx, tx, conversation, user); e != nil {
		return e
	}
	var owner string
	var message *string
	e = tx.QueryRow(ctx, "SELECT uploader_id,message_id FROM attachments WHERE id=$1 AND conversation_id=$2 FOR UPDATE", id, conversation).Scan(&owner, &message)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if owner != user || message != nil {
		return api.Err(404, "not_found", "This pending file is unavailable")
	}
	if _, e = tx.Exec(ctx, "DELETE FROM attachments WHERE id=$1", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Called in the message transaction, after the conversation and sender key are
// locked. Every file must be unexpired, unconsumed, and uploaded by this sender.
func attachFiles(ctx context.Context, tx pgx.Tx, user, conversation, message string, ids []string) ([]Attachment, error) {
	out := []Attachment{}
	for _, id := range ids {
		var a Attachment
		e := tx.QueryRow(ctx, `UPDATE attachments SET message_id=$1 WHERE id=$2 AND conversation_id=$3 AND uploader_id=$4 AND message_id IS NULL AND expires_at>now() RETURNING id,filename,media_type,size_bytes,sha256`, message, id, conversation, user).Scan(&a.ID, &a.Filename, &a.MediaType, &a.SizeBytes, &a.SHA256)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, api.Err(400, "invalid_attachment", "A file is unavailable, expired, or already attached")
		}
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *Store) DownloadAttachment(ctx context.Context, user, conversation, id string) (Attachment, []byte, error) {
	var a Attachment
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return a, nil, e
	}
	defer tx.Rollback(ctx)
	// Sharing the workspace/conversation locks orders revocation and retraction
	// with the authorized read. Content is never fetched before the ACL check.
	if _, e = lockConversation(ctx, tx, conversation, user); e != nil {
		return a, nil, e
	}
	var content []byte
	e = tx.QueryRow(ctx, `SELECT a.id,a.filename,a.media_type,a.size_bytes,a.sha256,a.content FROM attachments a LEFT JOIN messages m ON m.id=a.message_id WHERE a.id=$1 AND a.conversation_id=$2 AND ((a.message_id IS NULL AND a.uploader_id=$3 AND a.expires_at>now()) OR (a.message_id IS NOT NULL AND m.retracted_at IS NULL))`, id, conversation, user).Scan(&a.ID, &a.Filename, &a.MediaType, &a.SizeBytes, &a.SHA256, &content)
	if e != nil {
		return a, nil, notFound(e)
	}
	return a, content, tx.Commit(ctx)
}

func (h *Handler) attachmentRoutes(auth func(string, api.AuthedHandler)) {
	auth("POST /api/v1/conversations/{id}/attachments", func(w http.ResponseWriter, r *http.Request, session api.Session) error {
		if e := h.Store.requireFlag("im.attachments.upload"); e != nil {
			return e
		}
		if _, e := conversationAccess(r.Context(), h.Store.DB, r.PathValue("id"), session.User.ID); e != nil {
			return e
		}
		release, e := acquireAttachmentTransfer()
		if e != nil {
			return e
		}
		defer release()
		r.Body = http.MaxBytesReader(w, r.Body, imlimits.AttachmentRequestBytes)
		reader, e := r.MultipartReader()
		if e != nil {
			return api.Err(400, "invalid_upload", "Use multipart/form-data with one file field")
		}
		part, e := reader.NextPart()
		if e != nil {
			return api.Err(400, "invalid_upload", "One file is required")
		}
		defer part.Close()
		_, params, parseErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		// Do not use multipart.Part.FileName: it strips traversal prefixes.
		filename := params["filename"]
		if parseErr != nil || part.FormName() != "file" || validateFilename(filename) != nil {
			return api.Err(400, "invalid_filename", "Provide one file with a safe filename")
		}
		content, e := io.ReadAll(io.LimitReader(part, imlimits.AttachmentBytes+1))
		if e != nil || len(content) > imlimits.AttachmentBytes {
			return api.Err(413, "attachment_too_large", "File exceeds 4 MiB")
		}
		if _, e = reader.NextPart(); e != io.EOF {
			return api.Err(400, "invalid_upload", "Only one file per upload is supported")
		}
		a, e := h.Store.UploadAttachment(r.Context(), session.User.ID, r.PathValue("id"), filename, content)
		if e != nil {
			return e
		}
		api.Write(w, 201, map[string]any{"attachment": a})
		return nil
	})
	auth("DELETE /api/v1/conversations/{id}/attachments/{attachmentId}", func(w http.ResponseWriter, r *http.Request, session api.Session) error {
		if e := h.Store.DeletePendingAttachment(r.Context(), session.User.ID, r.PathValue("id"), r.PathValue("attachmentId")); e != nil {
			return e
		}
		api.Write(w, 204, nil)
		return nil
	})
	auth("GET /api/v1/conversations/{id}/attachments/{attachmentId}/content", func(w http.ResponseWriter, r *http.Request, session api.Session) error {
		release, e := acquireAttachmentTransfer()
		if e != nil {
			return e
		}
		defer release()
		a, content, e := h.Store.DownloadAttachment(r.Context(), session.User.ID, r.PathValue("id"), r.PathValue("attachmentId"))
		if e != nil {
			return e
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.Header().Set("X-Attachment-SHA256", a.SHA256)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		if _, e = w.Write(content); e != nil {
			return nil
		}
		// Hertz's Content-Length adaptor buffers Write until Flush. Keep the
		// transfer slot until that synchronous network write completes or fails,
		// rather than letting slow peers retain buffers after releasing capacity.
		// The binary response has begun: a write/flush failure must never append
		// the auth wrapper's JSON error body. Hertz also captures Flusher errors
		// internally, so ResponseController cannot report those errors here.
		_ = http.NewResponseController(w).Flush()
		return nil
	})
}
