package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/pkg/imlimits"
)

func TestAttachmentsAtomicBindingReplayRetractionAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, ws, c, owner, member := setup(t)
	raw := []byte{0, 255, 254, 'a', 0, 128}
	a, e := s.UploadAttachment(ctx, owner.ID, c.ID, "中文 <file>.bin", raw)
	if e != nil || a.MediaType != "application/octet-stream" || a.SizeBytes != len(raw) {
		t.Fatal(a, e)
	}
	if _, _, e = s.DownloadAttachment(ctx, member.ID, c.ID, a.ID); e == nil {
		t.Fatal("other member read an unlinked upload")
	}
	if _, _, e = s.SendWithAttachments(ctx, member, c.ID, "steal", "", nil, []string{a.ID}); e == nil {
		t.Fatal("other member attached upload")
	}
	if _, _, e = s.SendWithAttachments(ctx, owner, c.ID, "atomic", "", nil, []string{a.ID, api.ID()}); e == nil {
		t.Fatal("invalid attachment accepted")
	}
	var seq int
	if e = s.DB.QueryRow(ctx, "SELECT last_seq FROM conversations WHERE id=$1", c.ID).Scan(&seq); e != nil || seq != 0 {
		t.Fatal("invalid send consumed sequence", seq, e)
	}
	var linked *string
	if e = s.DB.QueryRow(ctx, "SELECT message_id FROM attachments WHERE id=$1", a.ID).Scan(&linked); e != nil || linked != nil {
		t.Fatal("partial file association", e)
	}
	m, first, e := s.SendWithAttachments(ctx, owner, c.ID, "atomic", "", nil, []string{a.ID})
	if e != nil || !first || len(m.Attachments) != 1 || m.Seq != 1 {
		t.Fatal("file-only send", m, e)
	}
	if _, data, e := s.DownloadAttachment(ctx, member.ID, c.ID, a.ID); e != nil || !bytes.Equal(data, raw) {
		t.Fatal("byte equality", e)
	}
	if _, _, e = s.SendWithAttachments(ctx, owner, c.ID, "reuse", "", nil, []string{a.ID}); e == nil {
		t.Fatal("attachment reused in another message")
	}
	retry, first, e := s.SendWithAttachments(ctx, owner, c.ID, "atomic", "", nil, []string{a.ID})
	if e != nil || first || retry.ID != m.ID || len(retry.Attachments) != 1 {
		t.Fatal("attachment retry", e)
	}
	if _, _, e = s.SendWithAttachments(ctx, owner, c.ID, "atomic", "changed", nil, []string{a.ID}); e == nil {
		t.Fatal("conflicting retry accepted")
	}
	if _, _, e = s.Send(ctx, owner, c.ID, "atomic", "body", nil); e == nil {
		t.Fatal("conflicting attachment list accepted")
	}
	history, _, e := s.Messages(ctx, member.ID, c.ID, 0, 50)
	if e != nil || len(history) != 1 || len(history[0].Attachments) != 1 {
		t.Fatal("history descriptors", e)
	}
	events, _, _, e := s.Sync(ctx, member.ID, "0", 100)
	if e != nil {
		t.Fatal(e)
	}
	seen := false
	for _, ev := range events {
		if ev.Message != nil && ev.Message.ID == m.ID {
			seen = len(ev.Message.Attachments) == 1
		}
	}
	if !seen {
		t.Fatal("reconnect omitted descriptor")
	}
	if _, e = s.ChangeMessage(ctx, owner.ID, c.ID, m.ID, "", false); e != nil {
		t.Fatal("file-only text edit", e)
	}
	if e = s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, member.ID, c.ID, a.ID); e == nil {
		t.Fatal("revoked member downloaded file")
	}
	if _, e = s.ChangeMessage(ctx, owner.ID, c.ID, m.ID, "", true); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, owner.ID, c.ID, a.ID); e == nil {
		t.Fatal("retracted content readable")
	}
	var n int
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM attachments WHERE id=$1", a.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("retracted binary retained", n, e)
	}
	events, _, _, e = s.Sync(ctx, owner.ID, "0", 100)
	for _, ev := range events {
		if ev.Message != nil && ev.Message.ID == m.ID && len(ev.Message.Attachments) > 0 {
			t.Fatal("old sync resurrected attachment")
		}
	}
	retry, first, e = s.SendWithAttachments(ctx, owner, c.ID, "atomic", "", nil, []string{a.ID})
	if e != nil || first || retry.ID != m.ID || retry.RetractedAt == nil || len(retry.Attachments) > 0 {
		t.Fatal("retracted send retry", e)
	}
}

func TestAttachmentWriteFlagExpiryQuotasAndConversationScope(t *testing.T) {
	ctx := context.Background()
	s, ws, c, owner, member := setup(t)
	other, e := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Private", []string{member.ID})
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.UploadAttachment(ctx, owner.ID, c.ID, "a.bin", []byte{0, 255})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, owner.ID, other.ID, a.ID); e == nil {
		t.Fatal("cross-conversation download")
	}
	if _, _, e = s.SendWithAttachments(ctx, owner, other.ID, "foreign", "", nil, []string{a.ID}); e == nil {
		t.Fatal("cross-conversation binding")
	}
	if _, e = s.DB.Exec(ctx, "UPDATE attachments SET expires_at=now()-interval '1 second' WHERE id=$1", a.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, owner.ID, c.ID, a.ID); e == nil {
		t.Fatal("expired pending readable")
	}
	if _, _, e = s.SendWithAttachments(ctx, owner, c.ID, "expired", "", nil, []string{a.ID}); e == nil {
		t.Fatal("expired binding")
	}
	a, e = s.UploadAttachment(ctx, owner.ID, c.ID, "b.bin", []byte{0, 255})
	if e != nil {
		t.Fatal(e)
	}
	m, _, e := s.SendWithAttachments(ctx, owner, c.ID, "persist", "", nil, []string{a.ID})
	if e != nil {
		t.Fatal(e)
	}
	pendingFile, e := s.UploadAttachment(ctx, owner.ID, c.ID, "pending.bin", []byte{1})
	if e != nil {
		t.Fatal(e)
	}
	flagPath := filepath.Join(t.TempDir(), "flags.json")
	if e = os.WriteFile(flagPath, []byte(`{"revision":"off","flags":{}}`), 0600); e != nil {
		t.Fatal(e)
	}
	s.Flags = config.Flags{Path: flagPath}
	if _, e = s.UploadAttachment(ctx, owner.ID, c.ID, "blocked", []byte{1}); e == nil {
		t.Fatal("flag off upload accepted")
	}
	if _, _, e = s.SendWithAttachments(ctx, owner, c.ID, "flag-blocked", "", nil, []string{pendingFile.ID}); e == nil {
		t.Fatal("flag off admitted new attachment message")
	}
	if e = s.DeletePendingAttachment(ctx, owner.ID, c.ID, pendingFile.ID); e != nil {
		t.Fatal("flag off blocked pending cleanup", e)
	}
	if _, b, e := s.DownloadAttachment(ctx, member.ID, c.ID, a.ID); e != nil || len(b) != 2 {
		t.Fatal("flag off hid content", e)
	}
	if got, e := s.Message(ctx, member.ID, c.ID, m.ID); e != nil || len(got.Attachments) != 1 {
		t.Fatal("flag off hid descriptors", e)
	}
	if _, first, e := s.SendWithAttachments(ctx, owner, c.ID, "persist", "", nil, []string{a.ID}); e != nil || first {
		t.Fatal("flag off broke committed retry", e)
	}
	s.Flags = config.Flags{}
	for i := 0; i < imlimits.PendingAttachmentsPerSender; i++ {
		if _, e = s.UploadAttachment(ctx, owner.ID, c.ID, "quota", []byte{1}); e != nil {
			t.Fatal(i, e)
		}
	}
	if _, e = s.UploadAttachment(ctx, owner.ID, c.ID, "over", []byte{1}); e == nil {
		t.Fatal("pending quota bypass")
	}
}

func TestAttachmentUsesCurrentAgentDelegationAndPrivateMembership(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, grantingMember := setup(t)
	a := api.User{ID: api.ID(), DisplayName: "Agent", Kind: "agent"}
	if _, e := s.SetPrincipalWorkspaces(ctx, a, grantingMember, []string{ws.ID}, 1); e != nil {
		t.Fatal(e)
	}
	file, e := s.UploadAttachment(ctx, owner.ID, channel.ID, "source.bin", []byte{0, 255})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.SendWithAttachments(ctx, owner, channel.ID, "agent-source", "", nil, []string{file.ID}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, a.ID, channel.ID, file.ID); e != nil {
		t.Fatal("authorized Agent file read", e)
	}
	group, e := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Private", []string{grantingMember.ID})
	if e != nil {
		t.Fatal(e)
	}
	private, e := s.UploadAttachment(ctx, owner.ID, group.ID, "private.bin", []byte{0, 128})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.SendWithAttachments(ctx, owner, group.ID, "agent-private", "", nil, []string{private.ID}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, a.ID, group.ID, private.ID); e == nil {
		t.Fatal("workspace grant bypassed private membership")
	}
	if _, e = s.GroupMember(ctx, owner.ID, group.ID, a.ID, true); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, a.ID, group.ID, private.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GroupMember(ctx, owner.ID, group.ID, a.ID, false); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, a.ID, group.ID, private.ID); e == nil {
		t.Fatal("moderated removal retained file read")
	}
	if e = s.RemoveMember(ctx, owner.ID, ws.ID, grantingMember.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DownloadAttachment(ctx, a.ID, channel.ID, file.ID); e == nil {
		t.Fatal("owner delegation revocation retained file read")
	}
}

func TestAttachmentConcurrentBindingConsumesOnce(t *testing.T) {
	ctx := context.Background()
	s, _, c, owner, _ := setup(t)
	a, e := s.UploadAttachment(ctx, owner.ID, c.ID, "once", []byte{1})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for _, key := range []string{"one", "two"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, _, e := s.SendWithAttachments(ctx, owner, c.ID, key, "", nil, []string{a.ID})
			wins <- e == nil
		}(key)
	}
	wg.Wait()
	close(wins)
	count := 0
	for won := range wins {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatal("concurrent attachment binding", count)
	}
}

type attachmentIdentity struct{ session api.Session }

func (f attachmentIdentity) Validate(context.Context, string) (api.Session, error) {
	return f.session, nil
}
func (f attachmentIdentity) Users(context.Context, []string) ([]api.User, error) { return nil, nil }
func (f attachmentIdentity) Ping(context.Context) error                          { return nil }

func TestMultipartAttachmentAuthorizationLimitsAndSafeDownload(t *testing.T) {
	s, _, c, owner, _ := setup(t)
	h := (&Handler{Store: s, Identity: attachmentIdentity{api.Session{User: owner, CSRFToken: "csrf"}}, Origin: "http://localhost"}).Routes()
	upload := func(name string, raw []byte, csrf bool) *httptest.ResponseRecorder {
		var b bytes.Buffer
		writer := multipart.NewWriter(&b)
		part, _ := writer.CreateFormFile("file", name)
		_, _ = part.Write(raw)
		_ = writer.Close()
		r := httptest.NewRequest("POST", "/api/v1/conversations/"+c.ID+"/attachments", &b)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		r.Header.Set("Origin", "http://localhost")
		if csrf {
			r.Header.Set("X-CSRF-Token", "csrf")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := upload("safe", []byte{1}, false); w.Code != 403 {
		t.Fatal("upload missing CSRF", w.Code)
	}
	for _, name := range []string{"../x", "a\\b", "bad\x01name", strings.Repeat("x", 181)} {
		if w := upload(name, []byte{1}, true); w.Code != 400 {
			t.Fatal("unsafe filename", name, w.Code, w.Body.String())
		}
	}
	if w := upload("empty", nil, true); w.Code != 413 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := upload("large", make([]byte, imlimits.AttachmentBytes+1), true); w.Code != 413 {
		t.Fatal(w.Code, w.Body.String())
	}
	raw := append([]byte{0, 255, 128}, bytes.Repeat([]byte("binary"), 23000)...)
	w := upload("<img src=x onerror=alert(1)>.html", raw, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		Attachment Attachment `json:"attachment"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/conversations/"+c.ID+"/attachments/"+out.Attachment.ID+"/content", nil))
	data, _ := io.ReadAll(w.Result().Body)
	if w.Code != 200 || !bytes.Equal(data, raw) || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("unsafe download", w.Code, w.Header())
	}
	release1, _ := acquireAttachmentTransfer()
	release2, _ := acquireAttachmentTransfer()
	defer release1()
	defer release2()
	if w := upload("busy", []byte{1}, true); w.Code != 429 {
		t.Fatal("unbounded parallel transfer", w.Code)
	}
}
