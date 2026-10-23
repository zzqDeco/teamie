package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"teamie/pkg/httpx"
	"teamie/pkg/config"
)

func uploadError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var failure *httpx.Error
	if !errors.As(err, &failure) || failure.Status != status || failure.Code != code {
		t.Fatalf("wanted %d %s, got %v", status, code, err)
	}
}

func TestAttachmentUploadHTTPReplayAndDiscoveryKeepThePublicContract(t *testing.T) {
	s, _, conversation, owner, member := setup(t)
	identity := &attachmentIdentity{httpx.Session{User: owner, CSRFToken: "csrf"}}
	h := (&Handler{Store: s, Identity: identity, Origin: "http://localhost"}).Routes()
	post := func(content []byte) *httptest.ResponseRecorder {
		var data bytes.Buffer
		writer := multipart.NewWriter(&data)
		part, err := writer.CreateFormFile("file", "结果.bin")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(content)
		_ = writer.Close()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+conversation.ID+"/attachments", &data)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Idempotency-Key", "client-file-1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := post([]byte{0, 255, 128})
	second := post([]byte{0, 255, 128})
	if first.Code != 201 || second.Code != 200 || first.Body.String() != second.Body.String() {
		t.Fatal("HTTP upload retry", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	var saved map[string]Attachment
	if err := json.Unmarshal(first.Body.Bytes(), &saved); err != nil || saved["attachment"].Filename != "结果.bin" {
		t.Fatal("public file descriptor", saved, err)
	}
	lookup := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/conversations/"+conversation.ID+"/attachment-uploads/client-file-1", nil))
		return w
	}
	if out := lookup(); out.Code != 200 || out.Body.String() != first.Body.String() {
		t.Fatal("HTTP lost-response discovery", out.Code, out.Body.String())
	}
	if out := post([]byte("different")); out.Code != 409 {
		t.Fatal("HTTP key conflict", out.Code, out.Body.String())
	}
	identity.session.User = member
	if out := lookup(); out.Code != 404 {
		t.Fatal("another uploader discovered a private receipt", out.Code)
	}
}

func TestAttachmentUploadConcurrentIdentityAndLostResponseDiscovery(t *testing.T) {
	ctx := context.Background()
	s, _, conversation, owner, member := setup(t)
	data := []byte{0, 255, 128, 1, 13, 10}
	const key = "stable-upload-1"
	var firsts atomic.Int32
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			a, first, err := s.UploadAttachmentWithKey(ctx, owner.ID, conversation.ID, "report.bin", data, key)
			if first {
				firsts.Add(1)
			}
			ids <- a.ID
			errs <- err
		})
	}
	group.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	known, err := s.LookupAttachmentUpload(ctx, owner.ID, conversation.ID, key)
	if err != nil || firsts.Load() != 1 {
		t.Fatal("upload identity", known, firsts.Load(), err)
	}
	for id := range ids {
		if id != known.ID {
			t.Fatal("concurrent retry created a different file", id, known.ID)
		}
	}
	_, content, err := s.DownloadAttachment(ctx, owner.ID, conversation.ID, known.ID)
	if err != nil || !bytes.Equal(content, data) {
		t.Fatal("binary upload changed bytes", err)
	}
	_, err = s.LookupAttachmentUpload(ctx, member.ID, conversation.ID, key)
	uploadError(t, err, 404, "attachment_upload_not_found")
	for _, change := range []struct {
		filename string
		content  []byte
	}{{"changed.bin", data}, {"report.bin", []byte("changed")}} {
		_, _, err = s.UploadAttachmentWithKey(ctx, owner.ID, conversation.ID, change.filename, change.content, key)
		uploadError(t, err, 409, "upload_conflict")
	}
	_, other, err := s.CreateWorkspace(ctx, owner.ID, "Other destination")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.UploadAttachmentWithKey(ctx, owner.ID, other.ID, "report.bin", data, key)
	uploadError(t, err, 409, "upload_conflict")
	message, _, err := s.SendWithAttachments(ctx, owner, conversation.ID, "file-only", "", nil, []string{known.ID})
	if err != nil || len(message.Attachments) != 1 {
		t.Fatal("file delivery", message, err)
	}
	// Recover an upload descriptor after linkage, without recreating or moving it.
	recovered, err := s.LookupAttachmentUpload(ctx, owner.ID, conversation.ID, key)
	if err != nil || recovered != known {
		t.Fatal("linked upload discovery", recovered, err)
	}
	flagFile := filepath.Join(t.TempDir(), "flags.json")
	if err = os.WriteFile(flagFile, []byte(`{"revision":"upload-disabled","flags":{"im.attachments.upload":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Flags = config.Flags{Path: flagFile}
	if _, err = s.LookupAttachmentUpload(ctx, owner.ID, conversation.ID, key); err != nil {
		t.Fatal("disabled writes hid a persisted upload", err)
	}
	recovered, first, err := s.UploadAttachmentWithKey(ctx, owner.ID, conversation.ID, "report.bin", data, key)
	if err != nil || first || recovered != known {
		t.Fatal("disabled admission broke committed retry", recovered, err)
	}
	_, _, err = s.UploadAttachmentWithKey(ctx, owner.ID, conversation.ID, "new.bin", data, "new-key")
	uploadError(t, err, 403, "feature_disabled")
	var count int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM attachment_uploads").Scan(&count); err != nil || count != 1 {
		t.Fatal("conflicts spent upload keys", count, err)
	}
}

func TestAttachmentUploadTombstonesSurviveRemovalExpiryAndRetraction(t *testing.T) {
	for _, action := range []string{"delete", "expire", "retract"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			s, _, conversation, owner, _ := setup(t)
			raw := []byte("same original bytes")
			a, _, err := s.UploadAttachmentWithKey(ctx, owner.ID, conversation.ID, "report.txt", raw, "key")
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "delete":
				err = s.DeletePendingAttachment(ctx, owner.ID, conversation.ID, a.ID)
			case "expire":
				_, err = s.DB.Exec(ctx, "UPDATE attachments SET expires_at=now()-interval '1 second' WHERE id=$1", a.ID)
			case "retract":
				message, _, sendErr := s.SendWithAttachments(ctx, owner, conversation.ID, "send", "", nil, []string{a.ID})
				if sendErr != nil {
					t.Fatal(sendErr)
				}
				_, err = s.ChangeMessage(ctx, owner.ID, conversation.ID, message.ID, "", true)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.LookupAttachmentUpload(ctx, owner.ID, conversation.ID, "key")
			uploadError(t, err, 410, "attachment_upload_unavailable")
			_, _, err = s.UploadAttachmentWithKey(ctx, owner.ID, conversation.ID, "report.txt", raw, "key")
			uploadError(t, err, 410, "attachment_upload_unavailable")
			if _, err = s.DB.Exec(ctx, "DELETE FROM attachments WHERE id=$1", a.ID); err != nil {
				t.Fatal(err)
			}
			_, err = s.LookupAttachmentUpload(ctx, owner.ID, conversation.ID, "key")
			uploadError(t, err, 410, "attachment_upload_unavailable")
			var kept int
			if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM attachment_uploads WHERE attachment_id IS NULL").Scan(&kept); err != nil || kept != 1 {
				t.Fatal("upload key became reusable after content cleanup", kept, err)
			}
		})
	}
}

func TestAttachmentUploadDiscoveryAndReplayRecheckCurrentMembership(t *testing.T) {
	ctx := context.Background()
	s, workspace, conversation, owner, member := setup(t)
	raw := []byte("private result")
	if _, _, err := s.UploadAttachmentWithKey(ctx, member.ID, conversation.ID, "private.txt", raw, "private-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, owner.ID, workspace.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.LookupAttachmentUpload(ctx, member.ID, conversation.ID, "private-key")
	uploadError(t, err, 404, "not_found")
	_, _, err = s.UploadAttachmentWithKey(ctx, member.ID, conversation.ID, "private.txt", raw, "private-key")
	uploadError(t, err, 404, "not_found")
}
