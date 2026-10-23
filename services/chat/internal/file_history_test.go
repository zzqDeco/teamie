package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"teamie/pkg/httpx"
)

func TestFileHistoryPagesPublishedGroupsAndPreservesReadACLs(t *testing.T) {
	ctx := context.Background()
	s, ws, conversation, owner, member := setup(t)
	var first, last Message
	for n := 1; n <= 70; n++ {
		var ids []string
		if n == 2 || n == 65 {
			names := []string{"Report 10%_.txt", "报告😀.pdf"}
			if n == 65 {
				names = []string{"report 100x.txt"}
			}
			for _, name := range names {
				file, err := s.UploadAttachment(ctx, owner.ID, conversation.ID, name, []byte{0, 255, 1})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, file.ID)
			}
		}
		message, _, err := s.SendWithAttachments(ctx, owner, conversation.ID, fmt.Sprintf("files-%d", n), "caption only", nil, ids)
		if err != nil {
			t.Fatal(err)
		}
		if n == 2 {
			first = message
		}
		if n == 65 {
			last = message
		}
	}
	// Pending uploads, including one's own, have not been delivered to IM.
	if _, err := s.UploadAttachment(ctx, owner.ID, conversation.ID, "Report pending.txt", []byte{1}); err != nil {
		t.Fatal(err)
	}
	page, more, err := s.FileHistory(ctx, member.ID, conversation.ID, 0, "REPORT", 1)
	if err != nil || !more || len(page) != 1 || page[0].ID != last.ID {
		t.Fatalf("first file page: %#v %v %v", page, more, err)
	}
	page, more, err = s.FileHistory(ctx, member.ID, conversation.ID, page[0].Seq, "report", 1)
	if err != nil || more || len(page) != 1 || page[0].ID != first.ID || len(page[0].Attachments) != 2 {
		t.Fatalf("distant grouped page: %#v %v %v", page, more, err)
	}
	for _, query := range []string{"10%_", "报告😀"} {
		page, _, err = s.FileHistory(ctx, owner.ID, conversation.ID, 0, query, 10)
		if err != nil || len(page) != 1 || page[0].ID != first.ID {
			t.Fatalf("literal Unicode filename %q: %#v %v", query, page, err)
		}
	}
	page, _, err = s.FileHistory(ctx, member.ID, conversation.ID, 0, "caption only", 10)
	if err != nil || len(page) != 0 {
		t.Fatal("file search matched captions", page, err)
	}
	page, _, err = s.MessageHistory(ctx, member.ID, conversation.ID, 0, 0, "Report", 10)
	if err != nil || len(page) != 0 {
		t.Fatal("ordinary body search changed semantics", page, err)
	}
	page, _, err = s.FileHistory(ctx, owner.ID, conversation.ID, 0, "", 10)
	if err != nil || len(page) != 2 {
		t.Fatal("file browse exposed pending or ordinary messages", page, err)
	}
	flagPath := filepath.Join(t.TempDir(), "flags.json")
	if err = os.WriteFile(flagPath, []byte(`{"revision":"off","flags":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Flags = httpx.Flags{Path: flagPath}
	page, _, err = s.FileHistory(ctx, member.ID, conversation.ID, 0, "report", 10)
	if err != nil || len(page) != 2 {
		t.Fatal("disabled writes hid published files", page, err)
	}
	s.Flags = httpx.Flags{}
	if _, err = s.ChangeMessage(ctx, owner.ID, conversation.ID, last.ID, "", true); err != nil {
		t.Fatal(err)
	}
	page, _, err = s.FileHistory(ctx, member.ID, conversation.ID, 0, "report", 10)
	if err != nil || len(page) != 1 || page[0].ID != first.ID {
		t.Fatal("retracted file remained searchable", page, err)
	}
	if _, _, err = s.FileHistory(ctx, httpx.ID(), conversation.ID, 0, "report", 10); err == nil {
		t.Fatal("outsider searched files")
	}
	if err = s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.FileHistory(ctx, member.ID, conversation.ID, 0, "report", 10); err == nil {
		t.Fatal("removed member searched files")
	}
}

func TestFileHistoryHTTPQueryValidationAndPrivateConversation(t *testing.T) {
	ctx := context.Background()
	s, ws, conversation, owner, member := setup(t)
	creator := httpx.User{ID: httpx.ID(), DisplayName: "Creator", Kind: "human"}
	code, _, err := s.CreateInvite(ctx, owner.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcceptInvite(ctx, creator.ID, code); err != nil {
		t.Fatal(err)
	}
	private, err := s.CreateConversation(ctx, creator.ID, ws.ID, "group", "Private files", []string{owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.UploadAttachment(ctx, owner.ID, conversation.ID, "<Report>.bin", []byte{0, 255})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.SendWithAttachments(ctx, owner, conversation.ID, "http-file", "", nil, []string{file.ID}); err != nil {
		t.Fatal(err)
	}
	handler := (&Handler{Store: s, Identity: attachmentIdentity{httpx.Session{User: member}}}).Routes()
	get := func(id, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/conversations/"+id+"/messages?"+query, nil)
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: "fixture"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := get(conversation.ID, "content=files&q=report&limit=1")
	var response struct {
		Messages []Message `json:"messages"`
		HasMore  bool      `json:"hasMore"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Messages) != 1 || response.Messages[0].Attachments[0].Filename != "<Report>.bin" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = get(private.ID, "content=files"); w.Code != 404 {
		t.Fatal("workspace member read private file list", w.Code, w.Body.String())
	}
	for _, query := range []string{"content=other", "content=files&aroundSeq=1", "content=files&beforeSeq=-1", "content=files&limit=101", "content=files&q=" + strings.Repeat("x", 201), "content=files&q=" + url.QueryEscape("nul\x00"), "q=%ff"} {
		if w = get(conversation.ID, query); w.Code != 400 {
			t.Fatal("invalid history query accepted", query, w.Code, w.Body.String())
		}
	}
}
