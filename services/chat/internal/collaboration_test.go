package chat

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/realtimeservice"
	"teamie/pkg/httpx"
	"teamie/pkg/rpcx"
	"teamie/services/chat/internal/infra/rpcclient"
)

type activeTestIdentity struct {
	mu       sync.RWMutex
	sessions map[string]httpx.User
	byID     map[string]httpx.User
	inactive map[string]bool
}

func newActiveTestIdentity(tokens map[string]httpx.User) *activeTestIdentity {
	byID := make(map[string]httpx.User, len(tokens))
	for _, u := range tokens {
		byID[u.ID] = u
	}
	return &activeTestIdentity{
		sessions: tokens,
		byID:     byID,
		inactive: map[string]bool{},
	}
}

func (f *activeTestIdentity) setInactive(userID string, off bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inactive[userID] = off
}

func (f *activeTestIdentity) Validate(_ context.Context, token string) (httpx.Session, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	u, ok := f.sessions[token]
	if !ok || f.inactive[u.ID] {
		return httpx.Session{}, httpx.Err(401, "unauthenticated", "invalid session")
	}
	return httpx.Session{User: u, CSRFToken: "csrf"}, nil
}

func (f *activeTestIdentity) Users(_ context.Context, ids []string) ([]httpx.User, error) {
	us, _, err := f.UsersWithActive(context.Background(), ids)
	return us, err
}

func (f *activeTestIdentity) UsersWithActive(_ context.Context, ids []string) ([]httpx.User, map[string]bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]httpx.User, 0, len(ids))
	active := make(map[string]bool, len(ids))
	for _, id := range ids {
		if u, ok := f.byID[id]; ok {
			out = append(out, u)
			active[id] = !f.inactive[id]
		}
	}
	return out, active, nil
}

func (f *activeTestIdentity) Ping(context.Context) error { return nil }

type testRealtimeRPC struct {
	mu     sync.RWMutex
	token  string
	states map[string]string
	fail   bool
}

func (s *testRealtimeRPC) GetPresence(_ context.Context, req *im.PresenceRequest) (*im.PresenceResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.fail || req == nil || s.token == "" || req.ServiceToken != s.token {
		return &im.PresenceResponse{Ok: false, ErrorCode: "forbidden_service", Presence: []*im.UserPresence{}}, nil
	}
	out := make([]*im.UserPresence, 0, len(req.UserIds))
	for _, id := range req.UserIds {
		if st := s.states[id]; st == "online" || st == "away" {
			out = append(out, &im.UserPresence{UserId: id, State: st})
		}
	}
	return &im.PresenceResponse{Ok: true, Presence: out}, nil
}

func (s *testRealtimeRPC) Ping(context.Context, *im.PingRequest) (*im.PingResponse, error) {
	return &im.PingResponse{Ready: true}, nil
}

func startTestRealtimeKitexServer(t *testing.T, svc *testRealtimeRPC) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()
	srv := realtimeservice.NewServer(svc, rpcx.ServerOptions(addr, 0)...)
	done := make(chan error, 1)
	go func() { done <- srv.Run() }()
	t.Cleanup(func() {
		_ = srv.Stop()
		<-done
	})
	return addr.String()
}

type collabHTTPClient struct {
	t      *testing.T
	srv    *httptest.Server
	origin string
}

func (c *collabHTTPClient) json(method, path, token string, body any) (int, map[string]any) {
	c.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, c.srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Cookie", httpx.CookieName+"="+token)
	req.Header.Set("Origin", c.origin)
	req.Header.Set("X-CSRF-Token", "csrf")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	var out map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	return res.StatusCode, out
}

func (c *collabHTTPClient) upload(convID, token, filename string, content []byte, idemKey string) (int, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err = fw.Write(content); err != nil {
		c.t.Fatal(err)
	}
	if err = mw.Close(); err != nil {
		c.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, c.srv.URL+"/api/v1/conversations/"+convID+"/attachments", &buf)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Cookie", httpx.CookieName+"="+token)
	req.Header.Set("Origin", c.origin)
	req.Header.Set("X-CSRF-Token", "csrf")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	res, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (c *collabHTTPClient) rawGet(path, token string) (int, http.Header, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodGet, c.srv.URL+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Cookie", httpx.CookieName+"="+token)
	res, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

func writeFlagsFile(t *testing.T, overrides map[string]bool) httpx.Flags {
	t.Helper()
	base := (httpx.Flags{}).Load().Flags
	for k, v := range overrides {
		base[k] = v
	}
	m := map[string]any{
		"revision": "test-flags",
		"flags":    base,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "flags.json")
	if err = os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return httpx.Flags{Path: p}
}

func encodePNG(t *testing.T, w, h int, fill color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEGWithExifOrientation(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y < h/4 {
				img.SetRGBA(x, y, color.RGBA{R: 220, G: 30, B: 30, A: 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 30, G: 80, B: 210, A: 255})
			}
		}
	}
	var rawJPEG bytes.Buffer
	if err := jpeg.Encode(&rawJPEG, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	src := rawJPEG.Bytes()
	// Build minimal APP1 Exif segment (big-endian TIFF with Orientation tag 0x0112 and secret marker).
	tiff := make([]byte, 26+len("SECRET_EXIF_COMMENT"))
	copy(tiff[0:2], "MM")
	binary.BigEndian.PutUint16(tiff[2:4], 0x002a)
	binary.BigEndian.PutUint32(tiff[4:8], 8)
	binary.BigEndian.PutUint16(tiff[8:10], 1)
	binary.BigEndian.PutUint16(tiff[10:12], 0x0112)
	binary.BigEndian.PutUint16(tiff[12:14], 3)
	binary.BigEndian.PutUint32(tiff[14:18], 1)
	binary.BigEndian.PutUint16(tiff[18:20], uint16(orientation))
	copy(tiff[26:], "SECRET_EXIF_COMMENT")

	app1Payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := len(app1Payload) + 2
	var out bytes.Buffer
	out.Write(src[:2]) // SOI
	out.Write([]byte{0xff, 0xe1, byte(segLen >> 8), byte(segLen)})
	out.Write(app1Payload)
	out.Write(src[2:])
	return out.Bytes()
}

func TestMessageForwardingSnapshotCopiesIsolationAndFlags(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, member := setup(t)
	peer := httpx.User{ID: httpx.ID(), DisplayName: "Peer", Kind: "human"}
	code, _, err := s.CreateInvite(ctx, owner.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcceptInvite(ctx, peer.ID, code); err != nil {
		t.Fatal(err)
	}
	// Private group containing ONLY owner and member (peer is NOT in sourceGroup).
	sourceGroup, err := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Secret Ops", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Second workspace for cross-workspace rejection tests.
	ws2, chan2, err := s.CreateWorkspace(ctx, owner.ID, "Other WS")
	if err != nil {
		t.Fatal(err)
	}
	_ = ws2

	ownerTok := strings.Repeat("1", 64)
	memberTok := strings.Repeat("2", 64)
	peerTok := strings.Repeat("3", 64)
	outsider := httpx.User{ID: httpx.ID(), DisplayName: "Outsider", Kind: "human"}
	outsiderTok := strings.Repeat("4", 64)
	id := newActiveTestIdentity(map[string]httpx.User{
		ownerTok:    owner,
		memberTok:   member,
		peerTok:     peer,
		outsiderTok: outsider,
	})
	origin := "https://teamie.example"
	srv := httptest.NewServer((&Handler{Store: s, Identity: id, Origin: origin}).Routes())
	defer srv.Close()
	hc := &collabHTTPClient{t: t, srv: srv, origin: origin}

	// Upload an image in sourceGroup and send a message with @all broadcast + image.
	pngBytes := encodePNG(t, 120, 80, color.NRGBA{R: 10, G: 200, B: 100, A: 180})
	st, upResp := hc.upload(sourceGroup.ID, ownerTok, "diagram.png", pngBytes, "up-src-1")
	if st != http.StatusCreated {
		t.Fatalf("upload status=%d resp=%v", st, upResp)
	}
	srcAtt := upResp["attachment"].(map[string]any)
	srcAttID := srcAtt["id"].(string)

	st, sendSrcResp := hc.json(http.MethodPost, "/api/v1/conversations/"+sourceGroup.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "src-msg-1",
		"body":            "@所有人 Architecture Spec v1",
		"attachmentIds":   []string{srcAttID},
		"mentions":        []map[string]any{{"kind": "all", "start": 0, "end": 4}},
	})
	if st != http.StatusCreated {
		t.Fatalf("send source status=%d resp=%v", st, sendSrcResp)
	}
	srcMsg := sendSrcResp["message"].(map[string]any)
	srcMsgID := srcMsg["id"].(string)

	// Invalid combinations: forwardOf + replyToId / attachmentIds -> 400 invalid_forward.
	if st, errBody := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "bad-fwd-reply",
		"body":            "bad",
		"replyToId":       srcMsgID,
		"forwardOf":       map[string]any{"conversationId": sourceGroup.ID, "messageId": srcMsgID},
	}); st != http.StatusBadRequest || errBody["error"].(map[string]any)["code"] != "invalid_forward" {
		t.Fatalf("expected 400 invalid_forward, got %d %v", st, errBody)
	}

	// Cross-workspace forward by authorized member -> 400 forward_cross_workspace; unreadable cross-workspace source -> 404.
	if st, errBody := hc.json(http.MethodPost, "/api/v1/conversations/"+chan2.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "cross-ws-fwd",
		"body":            "",
		"forwardOf":       map[string]any{"conversationId": sourceGroup.ID, "messageId": srcMsgID},
	}); st != http.StatusBadRequest || errBody["error"].(map[string]any)["code"] != "forward_cross_workspace" {
		t.Fatalf("expected 400 forward_cross_workspace, got %d %v", st, errBody)
	}
	if st, _ := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", peerTok, map[string]any{
		"clientMessageId": "peer-unreadable-cross-ws",
		"body":            "",
		"forwardOf":       map[string]any{"conversationId": chan2.ID, "messageId": "missing"},
	}); st != http.StatusNotFound {
		t.Fatalf("expected 404 for unreadable cross-workspace source, got %d", st)
	}

	// Forward sourceGroup message into workspace channel with empty comment -> 201.
	st, fwdResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", memberTok, map[string]any{
		"clientMessageId": "fwd-1",
		"body":            "@Peer FYI!",
		"mentions":        []map[string]any{{"userId": peer.ID, "start": 0, "end": 5}},
		"forwardOf":       map[string]any{"conversationId": sourceGroup.ID, "messageId": srcMsgID},
	})
	if st != http.StatusCreated {
		t.Fatalf("forward status=%d resp=%v", st, fwdResp)
	}
	fwdMsg := fwdResp["message"].(map[string]any)
	fwdMsgID := fwdMsg["id"].(string)
	snap := fwdMsg["forwarded"].(map[string]any)
	if snap["sourceConversationId"] != sourceGroup.ID || snap["sourceMessageId"] != srcMsgID || snap["senderId"] != owner.ID {
		t.Fatalf("unexpected snapshot metadata: %v", snap)
	}
	// Snapshot broadcast mention must strip recipientIds and keep only kind, start, end.
	snapMentions := snap["mentions"].([]any)
	snapMention0 := snapMentions[0].(map[string]any)
	if snapMention0["kind"] != "all" {
		t.Fatalf("expected kind=all on snapshot mention, got %v", snapMention0)
	}
	if _, leaked := snapMention0["recipientIds"]; leaked {
		t.Fatalf("snapshot broadcast mention leaked recipientIds: %v", snapMention0)
	}
	// Copied attachment has a new ID in target channel, preserving sha256 and image dimensions.
	snapAtts := snap["attachments"].([]any)
	copiedAtt := snapAtts[0].(map[string]any)
	copiedAttID := copiedAtt["id"].(string)
	if copiedAttID == srcAttID || copiedAtt["sha256"] != srcAtt["sha256"] {
		t.Fatalf("expected independent copied attachment row, got %v vs %v", copiedAtt, srcAtt)
	}
	copiedImg := copiedAtt["image"].(map[string]any)
	if int(copiedImg["width"].(float64)) != 120 || int(copiedImg["height"].(float64)) != 80 {
		t.Fatalf("copied image dimensions mismatch: %v", copiedImg)
	}

	// Peer (in channel, NOT in sourceGroup) can download copied content & thumbnail via channel, NOT via sourceGroup!
	if code, _, body := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+copiedAttID+"/content", peerTok); code != 200 || !bytes.Equal(body, pngBytes) {
		t.Fatalf("peer failed to download copied attachment: code=%d len=%d", code, len(body))
	}
	if code, hdr, thumb := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+copiedAttID+"/thumbnail", peerTok); code != 200 || hdr.Get("Content-Type") != "image/png" || len(thumb) == 0 {
		t.Fatalf("peer failed to download copied thumbnail: code=%d ct=%s", code, hdr.Get("Content-Type"))
	}
	if code, _, _ := hc.rawGet("/api/v1/conversations/"+sourceGroup.ID+"/attachments/"+srcAttID+"/content", peerTok); code != 404 {
		t.Fatalf("peer bypassed sourceGroup ACL: code=%d", code)
	}
	if st, _ := hc.json(http.MethodGet, "/api/v1/conversations/"+sourceGroup.ID+"/messages/"+srcMsgID, peerTok, nil); st != 404 {
		t.Fatalf("peer read sourceGroup message: status=%d", st)
	}

	// Edit and then retract source message: forward snapshot & copied attachment/thumbnail remain intact!
	if st, _ := hc.json(http.MethodPatch, "/api/v1/conversations/"+sourceGroup.ID+"/messages/"+srcMsgID, ownerTok, map[string]any{
		"body": "Edited later",
	}); st != 200 {
		t.Fatalf("edit source failed: %d", st)
	}
	if st, _ := hc.json(http.MethodDelete, "/api/v1/conversations/"+sourceGroup.ID+"/messages/"+srcMsgID, ownerTok, nil); st != 200 {
		t.Fatalf("retract source failed: %d", st)
	}
	// Forwarding a retracted source directly returns 409 message_retracted.
	if st, errBody := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "fwd-retracted-src",
		"body":            "",
		"forwardOf":       map[string]any{"conversationId": sourceGroup.ID, "messageId": srcMsgID},
	}); st != http.StatusConflict || errBody["error"].(map[string]any)["code"] != "message_retracted" {
		t.Fatalf("expected 409 message_retracted, got %d %v", st, errBody)
	}

	// Forwarding the existing forward (1-level flattening) STILL works after original source was retracted!
	st, nestedResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", peerTok, map[string]any{
		"clientMessageId": "fwd-nested-1",
		"body":            "",
		"forwardOf":       map[string]any{"conversationId": channel.ID, "messageId": fwdMsgID},
	})
	if st != http.StatusCreated {
		t.Fatalf("nested forward after original source retraction failed: %d %v", st, nestedResp)
	}
	nestedSnap := nestedResp["message"].(map[string]any)["forwarded"].(map[string]any)
	if nestedSnap["sourceConversationId"] != sourceGroup.ID || nestedSnap["sourceMessageId"] != srcMsgID || nestedSnap["body"] != "@所有人 Architecture Spec v1" {
		t.Fatalf("nested forward did not preserve flattened original snapshot: %v", nestedSnap)
	}
	nestedAttID := nestedSnap["attachments"].([]any)[0].(map[string]any)["id"].(string)
	if code, _, body := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+nestedAttID+"/content", peerTok); code != 200 || !bytes.Equal(body, pngBytes) {
		t.Fatalf("nested copied attachment unreadable: code=%d", code)
	}

	// Search & file-history match snapshot body and copied attachment filename!
	if st, hist := hc.json(http.MethodGet, "/api/v1/conversations/"+channel.ID+"/messages?q=Architecture+Spec", peerTok, nil); st != 200 || len(hist["messages"].([]any)) != 2 {
		t.Fatalf("history q did not match forward snapshots: %d %v", st, hist)
	}
	if st, files := hc.json(http.MethodGet, "/api/v1/conversations/"+channel.ID+"/messages?content=files&q=diagram", peerTok, nil); st != 200 || len(files["messages"].([]any)) != 2 {
		t.Fatalf("file history did not match forward snapshot attachments: %d %v", st, files)
	}
	if st, wsSearch := hc.json(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/messages/search?q=Architecture+Spec", peerTok, nil); st != 200 || len(wsSearch["items"].([]any)) != 2 {
		t.Fatalf("workspace search did not match forward snapshots: %d %v", st, wsSearch)
	}
	// Original sender (owner) gets NO attention from someone forwarding owner's message, while peer (mentioned in comment) DOES.
	if st, ownerAtt := hc.json(http.MethodGet, "/api/v1/attention", ownerTok, nil); st != 200 || int(ownerAtt["unreadCount"].(float64)) != 0 {
		t.Fatalf("original sender unexpectedly received attention from forward: %d %v", st, ownerAtt)
	}
	if st, peerAtt := hc.json(http.MethodGet, "/api/v1/attention", peerTok, nil); st != 200 || int(peerAtt["unreadCount"].(float64)) != 1 {
		t.Fatalf("comment mention on forward did not produce attention for peer: %d %v", st, peerAtt)
	}

	// Disable im.messages.forward: committed retry still returns 200; new forward returns 403; conflicting forwardOf returns 409.
	s.Flags = writeFlagsFile(t, map[string]bool{"im.messages.forward": false})
	if st, retryResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", memberTok, map[string]any{
		"clientMessageId": "fwd-1",
		"body":            "@Peer FYI!",
		"mentions":        []map[string]any{{"userId": peer.ID, "start": 0, "end": 5}},
		"forwardOf":       map[string]any{"conversationId": sourceGroup.ID, "messageId": srcMsgID},
	}); st != http.StatusOK || retryResp["message"].(map[string]any)["id"] != fwdMsgID {
		t.Fatalf("committed forward retry failed after source retraction & flag disable: %d %v", st, retryResp)
	}
	if st, conflictResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", memberTok, map[string]any{
		"clientMessageId": "fwd-1",
		"body":            "@Peer FYI!",
		"mentions":        []map[string]any{{"userId": peer.ID, "start": 0, "end": 5}},
		"forwardOf":       map[string]any{"conversationId": channel.ID, "messageId": fwdMsgID},
	}); st != http.StatusConflict || conflictResp["error"].(map[string]any)["code"] != "idempotency_conflict" {
		t.Fatalf("expected 409 idempotency_conflict on changed forwardOf, got %d %v", st, conflictResp)
	}
	if st, disabledResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", peerTok, map[string]any{
		"clientMessageId": "fwd-while-disabled",
		"body":            "",
		"forwardOf":       map[string]any{"conversationId": channel.ID, "messageId": fwdMsgID},
	}); st != http.StatusForbidden || disabledResp["error"].(map[string]any)["code"] != "feature_disabled" {
		t.Fatalf("expected 403 feature_disabled when im.messages.forward=false, got %d %v", st, disabledResp)
	}
	s.Flags = httpx.Flags{}

	// Retracting the forward removes snapshot, copied bytes, and copied thumbnail, and scrubs old sync events.
	if st, retResp := hc.json(http.MethodDelete, "/api/v1/conversations/"+channel.ID+"/messages/"+fwdMsgID, memberTok, nil); st != 200 {
		t.Fatalf("retract forward failed: %d %v", st, retResp)
	} else if _, hasFwd := retResp["message"].(map[string]any)["forwarded"]; hasFwd {
		t.Fatalf("retracted forward kept forwarded snapshot: %v", retResp)
	}
	if code, _, _ := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+copiedAttID+"/content", peerTok); code != 404 {
		t.Fatalf("retracted forward leaked copied attachment content: %d", code)
	}
	if code, _, _ := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+copiedAttID+"/thumbnail", peerTok); code != 404 {
		t.Fatalf("retracted forward leaked copied attachment thumbnail: %d", code)
	}
	if st, syncResp := hc.json(http.MethodGet, "/api/v1/sync?cursor=0", peerTok, nil); st != 200 {
		t.Fatalf("sync failed: %d", st)
	} else {
		for _, rawEv := range syncResp["events"].([]any) {
			ev := rawEv.(map[string]any)
			if msg, ok := ev["message"].(map[string]any); ok && msg["id"] == fwdMsgID {
				if _, hasSnap := msg["forwarded"]; hasSnap {
					t.Fatalf("old sync event leaked retracted forward snapshot: %v", ev)
				}
			}
		}
	}
}

func TestConversationPinsSavedItemsAndManualUnreadLifecycle(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, member := setup(t)
	group, err := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Design Team", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}

	ownerTok := strings.Repeat("a", 64)
	memberTok := strings.Repeat("b", 64)
	id := newActiveTestIdentity(map[string]httpx.User{
		ownerTok:  owner,
		memberTok: member,
	})
	origin := "https://teamie.example"
	srv := httptest.NewServer((&Handler{Store: s, Identity: id, Origin: origin}).Routes())
	defer srv.Close()
	hc := &collabHTTPClient{t: t, srv: srv, origin: origin}

	// Send 51 messages in channel to test pin ceiling, saved items, and manual unread.
	msgIDs := make([]string, 0, 51)
	for i := 1; i <= 51; i++ {
		m, _, sendErr := s.Send(ctx, owner, channel.ID, "msg-"+strconv.Itoa(i), "Message "+strconv.Itoa(i), nil)
		if sendErr != nil {
			t.Fatal(sendErr)
		}
		msgIDs = append(msgIDs, m.ID)
	}

	// --- 1. Pins ---
	st, pin1 := hc.json(http.MethodPut, "/api/v1/conversations/"+channel.ID+"/pins/"+msgIDs[0], memberTok, nil)
	if st != http.StatusOK || pin1["pinsVersion"] != "1" {
		t.Fatalf("first pin failed: %d %v", st, pin1)
	}
	// Re-pinning by owner is idempotent: preserves original pinnedBy (member) and pinsVersion "1".
	st, repin := hc.json(http.MethodPut, "/api/v1/conversations/"+channel.ID+"/pins/"+msgIDs[0], ownerTok, nil)
	if st != http.StatusOK || repin["pinsVersion"] != "1" || repin["pin"].(map[string]any)["pinnedBy"] != member.ID {
		t.Fatalf("repin not idempotent: %d %v", st, repin)
	}
	// Fill up to 50 pins, then verify 51st pin returns 409 pin_limit.
	for i := 1; i < 50; i++ {
		if st, pResp := hc.json(http.MethodPut, "/api/v1/conversations/"+channel.ID+"/pins/"+msgIDs[i], ownerTok, nil); st != 200 {
			t.Fatalf("pin %d failed: %d %v", i, st, pResp)
		}
	}
	if st, limResp := hc.json(http.MethodPut, "/api/v1/conversations/"+channel.ID+"/pins/"+msgIDs[50], ownerTok, nil); st != http.StatusConflict || limResp["error"].(map[string]any)["code"] != "pin_limit" {
		t.Fatalf("expected 409 pin_limit on 51st pin, got %d %v", st, limResp)
	}
	// Retracting a pinned message auto-unpins it and increments pinsVersion (50 -> 51).
	if st, _ := hc.json(http.MethodDelete, "/api/v1/conversations/"+channel.ID+"/messages/"+msgIDs[0], ownerTok, nil); st != 200 {
		t.Fatalf("retract pinned message failed: %d", st)
	}
	if st, listResp := hc.json(http.MethodGet, "/api/v1/conversations/"+channel.ID+"/pins", memberTok, nil); st != 200 || listResp["pinsVersion"] != "51" || len(listResp["pins"].([]any)) != 49 {
		t.Fatalf("expected 49 pins at version 51 after retraction, got %d %v", st, listResp)
	}
	// Pinning a retracted message returns 409 message_retracted.
	if st, errResp := hc.json(http.MethodPut, "/api/v1/conversations/"+channel.ID+"/pins/"+msgIDs[0], ownerTok, nil); st != http.StatusConflict || errResp["error"].(map[string]any)["code"] != "message_retracted" {
		t.Fatalf("expected 409 message_retracted on pinning retracted msg, got %d %v", st, errResp)
	}
	// Disable im.messages.pin: new pin returns 403, while list and DELETE unpin stay allowed!
	s.Flags = writeFlagsFile(t, map[string]bool{"im.messages.pin": false})
	if st, _ := hc.json(http.MethodPut, "/api/v1/conversations/"+channel.ID+"/pins/"+msgIDs[50], ownerTok, nil); st != http.StatusForbidden {
		t.Fatalf("expected 403 when im.messages.pin=false, got %d", st)
	}
	if st, delResp := hc.json(http.MethodDelete, "/api/v1/conversations/"+channel.ID+"/pins/"+msgIDs[1], memberTok, nil); st != 200 || delResp["pinsVersion"] != "52" {
		t.Fatalf("unpin should stay allowed when im.messages.pin=false: %d %v", st, delResp)
	}
	if st, listResp := hc.json(http.MethodGet, "/api/v1/conversations/"+channel.ID+"/pins", memberTok, nil); st != 200 || len(listResp["pins"].([]any)) != 48 {
		t.Fatalf("list pins should stay allowed when im.messages.pin=false: %d %v", st, listResp)
	}
	s.Flags = httpx.Flags{}

	// --- 2. Saved items ---
	grpMsg, _, err := s.Send(ctx, owner, group.ID, "grp-save-1", "Save me in group", nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerSyncBefore, err := s.Head(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	memberSyncBefore, err := s.Head(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}

	st, save1 := hc.json(http.MethodPut, "/api/v1/conversations/"+group.ID+"/messages/"+grpMsg.ID+"/saved", memberTok, nil)
	if st != 200 {
		t.Fatalf("save message failed: %d %v", st, save1)
	}
	savedAt1 := save1["saved"].(map[string]any)["savedAt"].(string)
	// Re-saving is idempotent and keeps original savedAt.
	if st, save2 := hc.json(http.MethodPut, "/api/v1/conversations/"+group.ID+"/messages/"+grpMsg.ID+"/saved", memberTok, nil); st != 200 || save2["saved"].(map[string]any)["savedAt"] != savedAt1 {
		t.Fatalf("re-save changed savedAt: %d %v", st, save2)
	}
	if st, saveChan := hc.json(http.MethodPut, "/api/v1/conversations/"+channel.ID+"/messages/"+msgIDs[2]+"/saved", memberTok, nil); st != 200 {
		t.Fatalf("save channel msg failed: %d %v", st, saveChan)
	}
	// Caller-only saved.updated sync event: member sees saved.updated, owner sees zero saved.updated events!
	if st, ownerSync := hc.json(http.MethodGet, "/api/v1/sync?cursor="+ownerSyncBefore, ownerTok, nil); st != 200 || len(ownerSync["events"].([]any)) != 0 {
		t.Fatalf("saved.updated leaked into owner sync stream: %d %v", st, ownerSync)
	}
	if st, memberSync := hc.json(http.MethodGet, "/api/v1/sync?cursor="+memberSyncBefore, memberTok, nil); st != 200 || len(memberSync["events"].([]any)) != 2 {
		t.Fatalf("expected 2 saved.updated events in member sync stream, got %d %v", st, memberSync)
	}
	// Pagination on GET /api/v1/saved and per-conversation IDs on GET /api/v1/conversations/:id/saved.
	if st, convSaved := hc.json(http.MethodGet, "/api/v1/conversations/"+group.ID+"/saved", memberTok, nil); st != 200 || len(convSaved["messageIds"].([]any)) != 1 {
		t.Fatalf("unexpected conversation saved IDs: %d %v", st, convSaved)
	}
	st, page1 := hc.json(http.MethodGet, "/api/v1/saved?limit=1", memberTok, nil)
	if st != 200 || len(page1["items"].([]any)) != 1 || page1["cursor"] == nil {
		t.Fatalf("saved page 1 failed: %d %v", st, page1)
	}
	st, page2 := hc.json(http.MethodGet, "/api/v1/saved?limit=1&cursor="+page1["cursor"].(string), memberTok, nil)
	if st != 200 || len(page2["items"].([]any)) != 1 {
		t.Fatalf("saved page 2 failed: %d %v", st, page2)
	}
	// Removing member from group deletes member's saved row in group; re-adding does NOT restore it!
	if _, err = s.GroupMember(ctx, owner.ID, group.ID, member.ID, false); err != nil {
		t.Fatal(err)
	}
	if st, afterKick := hc.json(http.MethodGet, "/api/v1/saved", memberTok, nil); st != 200 || len(afterKick["items"].([]any)) != 1 {
		t.Fatalf("expected group saved item omitted/deleted after group removal, got %d %v", st, afterKick)
	}
	if _, err = s.GroupMember(ctx, owner.ID, group.ID, member.ID, true); err != nil {
		t.Fatal(err)
	}
	if st, afterRejoin := hc.json(http.MethodGet, "/api/v1/conversations/"+group.ID+"/saved", memberTok, nil); st != 200 || len(afterRejoin["messageIds"].([]any)) != 0 {
		t.Fatalf("rejoining group must not restore deleted saved item: %d %v", st, afterRejoin)
	}
	// Disable im.messages.save: new save returns 403, while read and DELETE unsave -> 204 stay allowed!
	s.Flags = writeFlagsFile(t, map[string]bool{"im.messages.save": false})
	if st, _ := hc.json(http.MethodPut, "/api/v1/conversations/"+channel.ID+"/messages/"+msgIDs[3]+"/saved", memberTok, nil); st != http.StatusForbidden {
		t.Fatalf("expected 403 when im.messages.save=false, got %d", st)
	}
	if st, _ := hc.json(http.MethodDelete, "/api/v1/conversations/"+channel.ID+"/messages/"+msgIDs[2]+"/saved", memberTok, nil); st != http.StatusNoContent {
		t.Fatalf("expected 204 on unsave when im.messages.save=false, got %d", st)
	}
	s.Flags = httpx.Flags{}

	// --- 3. Manual unread marker ---
	// Advance member's read cursor to 10 first.
	if st, rResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/read", memberTok, map[string]any{"seq": 10}); st != 200 || int(rResp["readSeq"].(float64)) != 10 {
		t.Fatalf("mark read failed: %d %v", st, rResp)
	}
	// Invalid seq (0 or > lastSeq 51) -> 400 invalid_seq.
	if st, errResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/unread", memberTok, map[string]any{"seq": 0}); st != 400 || errResp["error"].(map[string]any)["code"] != "invalid_seq" {
		t.Fatalf("expected 400 invalid_seq for seq=0, got %d %v", st, errResp)
	}
	if st, errResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/unread", memberTok, map[string]any{"seq": 52}); st != 400 || errResp["error"].(map[string]any)["code"] != "invalid_seq" {
		t.Fatalf("expected 400 invalid_seq for seq>lastSeq, got %d %v", st, errResp)
	}
	memberSyncBeforeUnread, _ := s.Head(ctx, member.ID)
	// Mark unread at seq 5 (below readSeq 10!): readSeq stays 10, manualUnreadSeq becomes 5, read receipts stay at 10!
	if st, uResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/unread", memberTok, map[string]any{"seq": 5}); st != 200 || int(uResp["manualUnreadSeq"].(float64)) != 5 {
		t.Fatalf("mark unread failed: %d %v", st, uResp)
	}
	if st, rr := hc.json(http.MethodGet, "/api/v1/conversations/"+channel.ID+"/read-receipts", ownerTok, nil); st != 200 {
		t.Fatalf("read receipts failed: %d", st)
	} else if counts := rr["readSeqCounts"].([]any); len(counts) != 1 || int(counts[0].(map[string]any)["readSeq"].(float64)) != 10 {
		t.Fatalf("manual unread marker mutated monotonic read receipt cursor: %v", rr)
	}
	// Reading seq 4 (< manualUnreadSeq 5) keeps manualUnreadSeq=5.
	if st, rResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/read", memberTok, map[string]any{"seq": 4}); st != 200 || int(rResp["manualUnreadSeq"].(float64)) != 5 || int(rResp["readSeq"].(float64)) != 10 {
		t.Fatalf("read below marker should keep marker=5 and readSeq=10: %d %v", st, rResp)
	}
	// Disable im.unread.mark: new marker fails with 403, persisted marker stays visible, reading through seq 5 clears it!
	s.Flags = writeFlagsFile(t, map[string]bool{"im.unread.mark": false})
	if st, _ := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/unread", memberTok, map[string]any{"seq": 6}); st != http.StatusForbidden {
		t.Fatalf("expected 403 when im.unread.mark=false, got %d", st)
	}
	if st, convs := hc.json(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/conversations", memberTok, nil); st != 200 {
		t.Fatalf("list conversations failed: %d", st)
	} else {
		foundMarker := false
		for _, rawC := range convs["conversations"].([]any) {
			c := rawC.(map[string]any)
			if c["id"] == channel.ID && int(c["manualUnreadSeq"].(float64)) == 5 {
				foundMarker = true
			}
		}
		if !foundMarker {
			t.Fatalf("persisted manualUnreadSeq missing when im.unread.mark=false: %v", convs)
		}
	}
	// Reading seq 5 (>= manualUnreadSeq 5, even though < readSeq 10!) clears manualUnreadSeq atomically and emits read.updated with manualUnreadSeq: 0!
	if st, rResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/read", memberTok, map[string]any{"seq": 5}); st != 200 {
		t.Fatalf("read through marker failed: %d %v", st, rResp)
	} else if _, present := rResp["manualUnreadSeq"]; present {
		t.Fatalf("cleared manualUnreadSeq must be omitted from MarkReadResp: %v", rResp)
	}
	if st, syncResp := hc.json(http.MethodGet, "/api/v1/sync?cursor="+memberSyncBeforeUnread, memberTok, nil); st != 200 {
		t.Fatalf("sync failed: %d", st)
	} else {
		evs := syncResp["events"].([]any)
		if len(evs) != 2 {
			t.Fatalf("expected 2 read.updated events (set=5, clear=0), got %d %v", len(evs), evs)
		}
		ev0 := evs[0].(map[string]any)
		ev1 := evs[1].(map[string]any)
		if int(ev0["manualUnreadSeq"].(float64)) != 5 || int(ev1["manualUnreadSeq"].(float64)) != 0 {
			t.Fatalf("unexpected read.updated manualUnreadSeq values: %v / %v", ev0, ev1)
		}
	}
	s.Flags = httpx.Flags{}
}

func TestBroadcastMentionsAllHereParityPresenceFailureAndSingleMessageAttention(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, onlineHuman := setup(t)
	awayHuman := httpx.User{ID: httpx.ID(), DisplayName: "Away Human", Kind: "human"}
	offlineHuman := httpx.User{ID: httpx.ID(), DisplayName: "Offline Human", Kind: "human"}
	activeAgent := httpx.User{ID: httpx.ID(), DisplayName: "Active Bot", Kind: "agent"}
	disabledAgent := httpx.User{ID: httpx.ID(), DisplayName: "Disabled Bot", Kind: "agent"}

	for _, u := range []httpx.User{awayHuman, offlineHuman} {
		code, _, err := s.CreateInvite(ctx, owner.ID, ws.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.AcceptInvite(ctx, u.ID, code); err != nil {
			t.Fatal(err)
		}
	}
	// Admit both agents to workspace (automatically joins workspace channels).
	if _, err := s.SetPrincipalWorkspaces(ctx, activeAgent, owner, []string{ws.ID}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPrincipalWorkspaces(ctx, disabledAgent, owner, []string{ws.ID}, 1); err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateConversation(ctx, onlineHuman.ID, ws.ID, "group", "Incident Room", []string{owner.ID, awayHuman.ID, offlineHuman.ID, activeAgent.ID, disabledAgent.ID})
	if err != nil {
		t.Fatal(err)
	}

	ownerTok := strings.Repeat("1", 64)
	onlineTok := strings.Repeat("2", 64)
	awayTok := strings.Repeat("3", 64)
	offlineTok := strings.Repeat("4", 64)
	activeAgentTok := strings.Repeat("5", 64)
	disabledAgentTok := strings.Repeat("6", 64)

	id := newActiveTestIdentity(map[string]httpx.User{
		ownerTok:         owner,
		onlineTok:        onlineHuman,
		awayTok:          awayHuman,
		offlineTok:       offlineHuman,
		activeAgentTok:   activeAgent,
		disabledAgentTok: disabledAgent,
	})
	id.setInactive(disabledAgent.ID, true)

	rtSvc := &testRealtimeRPC{
		token: "rt-secret-token",
		states: map[string]string{
			owner.ID:       "online",
			onlineHuman.ID: "online",
			awayHuman.ID:   "away",
			// offlineHuman absent; agents have no IM presence.
		},
	}
	rtAddr := startTestRealtimeKitexServer(t, rtSvc)
	rtClient, err := rpcclient.NewRealtime(rtAddr, "rt-secret-token")
	if err != nil {
		t.Fatal(err)
	}
	s.Presence = rtClient

	origin := "https://teamie.example"
	srv := httptest.NewServer((&Handler{Store: s, Identity: id, Presence: rtClient, Origin: origin}).Routes())
	defer srv.Close()
	hc := &collabHTTPClient{t: t, srv: srv, origin: origin}

	// 1. Validation: broadcast entry with userId (even empty ""), unknown kind, or 2 broadcasts -> 400 invalid_mentions.
	for _, bad := range []any{
		[]map[string]any{{"userId": "", "kind": "all", "start": 0, "end": 4}},
		[]map[string]any{{"kind": "everyone", "start": 0, "end": 4}},
		[]map[string]any{{"kind": "all", "start": 0, "end": 4}, {"kind": "here", "start": 5, "end": 10}},
	} {
		if st, errResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
			"clientMessageId": httpx.ID(),
			"body":            "@所有人 @在线成员 test",
			"mentions":        bad,
		}); st != 400 || errResp["error"].(map[string]any)["code"] != "invalid_mentions" {
			t.Fatalf("expected 400 invalid_mentions for %v, got %d %v", bad, st, errResp)
		}
	}

	// 2. Permission: non-owner in channel gets 403 broadcast_forbidden; non-owner in group is allowed!
	if st, errResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", onlineTok, map[string]any{
		"clientMessageId": "non-owner-chan-all",
		"body":            "@所有人 hello",
		"mentions":        []map[string]any{{"kind": "all", "start": 0, "end": 4}},
	}); st != http.StatusForbidden || errResp["error"].(map[string]any)["code"] != "broadcast_forbidden" {
		t.Fatalf("expected 403 broadcast_forbidden for non-owner in channel, got %d %v", st, errResp)
	}
	if st, grpAll := hc.json(http.MethodPost, "/api/v1/conversations/"+group.ID+"/messages", onlineTok, map[string]any{
		"clientMessageId": "member-grp-all",
		"body":            "@所有人 group ping",
		"mentions":        []map[string]any{{"kind": "all", "start": 0, "end": 4}},
	}); st != http.StatusCreated {
		t.Fatalf("expected group member @all to succeed, got %d %v", st, grpAll)
	} else {
		m0 := grpAll["message"].(map[string]any)["mentions"].([]any)[0].(map[string]any)
		if len(m0["recipientIds"].([]any)) != 5 {
			t.Fatalf("expected 5 @all recipients (all 6 group members except sender), got %v", m0)
		}
	}

	// 3. @here in channel by owner: addresses onlineHuman + awayHuman + activeAgent (3 recipients),
	// excluding sender (owner), offlineHuman, and disabledAgent!
	st, hereResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "chan-here-1",
		"body":            "@在线成员 standup now",
		"mentions":        []map[string]any{{"kind": "here", "start": 0, "end": 5}},
	})
	if st != http.StatusCreated {
		t.Fatalf("owner @here failed: %d %v", st, hereResp)
	}
	hereMsg := hereResp["message"].(map[string]any)
	hereMsgID := hereMsg["id"].(string)
	hereMention := hereMsg["mentions"].([]any)[0].(map[string]any)
	if hereMention["kind"] != "here" || int(hereMention["recipientCount"].(float64)) != 3 {
		t.Fatalf("expected kind=here recipientCount=3, got %v", hereMention)
	}
	if _, leaked := hereMention["recipientIds"]; leaked {
		t.Fatalf("@here response leaked recipientIds: %v", hereMention)
	}

	// Verify sync and workspace search NEVER leak @here recipientIds either.
	if st, wsSearch := hc.json(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/messages/search?q=standup", onlineTok, nil); st != 200 {
		t.Fatalf("workspace search failed: %d", st)
	} else {
		sm := wsSearch["items"].([]any)[0].(map[string]any)["message"].(map[string]any)["mentions"].([]any)[0].(map[string]any)
		if _, leaked := sm["recipientIds"]; leaked {
			t.Fatalf("workspace search leaked @here recipientIds: %v", sm)
		}
	}

	// 4. GET /api/v1/attention?messageId= single-message lookup & global count preservation!
	for _, tc := range []struct {
		name      string
		tok       string
		addressed bool
	}{
		{"sender", ownerTok, false},
		{"onlineHuman", onlineTok, true},
		{"awayHuman", awayTok, true},
		{"offlineHuman", offlineTok, false},
		{"activeAgent", activeAgentTok, true},
	} {
		st, att := hc.json(http.MethodGet, "/api/v1/attention?messageId="+hereMsgID, tc.tok, nil)
		if st != 200 {
			t.Fatalf("%s attention?messageId failed: %d %v", tc.name, st, att)
		}
		items := att["items"].([]any)
		if tc.addressed && len(items) != 1 {
			t.Fatalf("expected %s addressed by @here, got items=%v", tc.name, items)
		}
		if !tc.addressed && len(items) != 0 {
			t.Fatalf("expected %s NOT addressed by @here, got items=%v", tc.name, items)
		}
	}
	// Combining messageId with cursor or unreadOnly=true returns 400 invalid_query.
	if st, _ := hc.json(http.MethodGet, "/api/v1/attention?messageId="+hereMsgID+"&unreadOnly=true", onlineTok, nil); st != 400 {
		t.Fatalf("expected 400 for messageId+unreadOnly=true, got %d", st)
	}
	if st, _ := hc.json(http.MethodGet, "/api/v1/attention?messageId="+hereMsgID+"&cursor=abc", onlineTok, nil); st != 400 {
		t.Fatalf("expected 400 for messageId+cursor, got %d", st)
	}

	// 5. Presence outage -> 503 presence_unavailable without consuming seq or clientMessageId!
	rtSvc.mu.Lock()
	rtSvc.fail = true
	rtSvc.mu.Unlock()
	if st, errResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "here-during-outage",
		"body":            "@在线成员 retryable",
		"mentions":        []map[string]any{{"kind": "here", "start": 0, "end": 5}},
	}); st != http.StatusServiceUnavailable || errResp["error"].(map[string]any)["code"] != "presence_unavailable" {
		t.Fatalf("expected 503 presence_unavailable during Realtime outage, got %d %v", st, errResp)
	}
	// Committed @here retry ("chan-here-1") STILL returns 200 during Realtime outage!
	if st, retryResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "chan-here-1",
		"body":            "@在线成员 standup now",
		"mentions":        []map[string]any{{"kind": "here", "start": 0, "end": 5}},
	}); st != http.StatusOK || retryResp["message"].(map[string]any)["id"] != hereMsgID {
		t.Fatalf("committed @here retry failed during outage: %d %v", st, retryResp)
	}
	// Restore Realtime: reusing "here-during-outage" succeeds with 201 and contiguous seq=2 (no gap!).
	rtSvc.mu.Lock()
	rtSvc.fail = false
	rtSvc.mu.Unlock()
	if st, recResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "here-during-outage",
		"body":            "@在线成员 retryable",
		"mentions":        []map[string]any{{"kind": "here", "start": 0, "end": 5}},
	}); st != http.StatusCreated || int(recResp["message"].(map[string]any)["seq"].(float64)) != 2 {
		t.Fatalf("expected contiguous seq=2 after failed @here retry, got %d %v", st, recResp)
	}

	// 6. Feature flags: im.presence.publish=false blocks @here (403) while @all works;
	// im.mentions.broadcast=false blocks new broadcasts while editing to REMOVE broadcast remains allowed!
	s.Flags = writeFlagsFile(t, map[string]bool{"im.presence.publish": false})
	if st, _ := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "here-presence-off",
		"body":            "@在线成员 off",
		"mentions":        []map[string]any{{"kind": "here", "start": 0, "end": 5}},
	}); st != http.StatusForbidden {
		t.Fatalf("expected 403 for @here when im.presence.publish=false, got %d", st)
	}
	if st, _ := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "all-presence-off",
		"body":            "@所有人 still works",
		"mentions":        []map[string]any{{"kind": "all", "start": 0, "end": 4}},
	}); st != http.StatusCreated {
		t.Fatalf("@all should not require im.presence.publish, got %d", st)
	}

	s.Flags = writeFlagsFile(t, map[string]bool{"im.mentions.broadcast": false})
	if st, boot := hc.json(http.MethodGet, "/api/v1/bootstrap", ownerTok, nil); st != 200 || boot["capabilities"].(map[string]any)["im.mentions.broadcast"] != false {
		t.Fatalf("expected bootstrap im.mentions.broadcast=false, got %d %v", st, boot)
	}
	if st, _ := hc.json(http.MethodPatch, "/api/v1/conversations/"+channel.ID+"/messages/"+hereMsgID, ownerTok, map[string]any{
		"body":     "@在线成员 keep broadcast while disabled",
		"mentions": []map[string]any{{"kind": "here", "start": 0, "end": 5}},
	}); st != http.StatusForbidden {
		t.Fatalf("expected 403 editing with broadcast when im.mentions.broadcast=false, got %d", st)
	}
	// Editing to REMOVE the broadcast entry succeeds even when im.mentions.broadcast=false, and clears attention!
	if st, edited := hc.json(http.MethodPatch, "/api/v1/conversations/"+channel.ID+"/messages/"+hereMsgID, ownerTok, map[string]any{
		"body": "plain text without broadcast",
	}); st != 200 {
		t.Fatalf("editing to remove broadcast should stay allowed when flag disabled: %d %v", st, edited)
	}
	if st, att := hc.json(http.MethodGet, "/api/v1/attention?messageId="+hereMsgID, onlineTok, nil); st != 200 || len(att["items"].([]any)) != 0 {
		t.Fatalf("expected @here attention cleared after edit removed broadcast: %d %v", st, att)
	}
	s.Flags = httpx.Flags{}
}

func TestImageMetadataExifOrientationAndThumbnailACLs(t *testing.T) {
	s, _, channel, owner, member := setup(t)
	outsider := httpx.User{ID: httpx.ID(), DisplayName: "Admin Outsider", Kind: "human", PlatformRole: "admin"}

	ownerTok := strings.Repeat("a", 64)
	memberTok := strings.Repeat("b", 64)
	outsiderTok := strings.Repeat("c", 64)
	id := newActiveTestIdentity(map[string]httpx.User{
		ownerTok:    owner,
		memberTok:   member,
		outsiderTok: outsider,
	})
	origin := "https://teamie.example"
	srv := httptest.NewServer((&Handler{Store: s, Identity: id, Origin: origin}).Routes())
	defer srv.Close()
	hc := &collabHTTPClient{t: t, srv: srv, origin: origin}

	// 1. Small alpha PNG (300x200 -> never upscaled, encoded as image/png).
	alphaPNG := encodePNG(t, 300, 200, color.NRGBA{R: 50, G: 120, B: 240, A: 128})
	st, pngResp := hc.upload(channel.ID, ownerTok, "badge.png", alphaPNG, "idem-png-1")
	if st != http.StatusCreated {
		t.Fatalf("png upload failed: %d %v", st, pngResp)
	}
	pngAtt := pngResp["attachment"].(map[string]any)
	pngID := pngAtt["id"].(string)
	pngImg := pngAtt["image"].(map[string]any)
	if int(pngImg["width"].(float64)) != 300 || int(pngImg["height"].(float64)) != 200 {
		t.Fatalf("expected 300x200 image metadata, got %v", pngImg)
	}
	// Retry with same Idempotency-Key returns 200 and preserves image metadata.
	if st, retryResp := hc.upload(channel.ID, ownerTok, "badge.png", alphaPNG, "idem-png-1"); st != http.StatusOK || retryResp["attachment"].(map[string]any)["image"] == nil {
		t.Fatalf("idempotent retry lost image metadata: %d %v", st, retryResp)
	}

	// Before linkage: uploader gets 200 with inline + safe headers; other member gets 404!
	code, hdr, thumbBytes := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+pngID+"/thumbnail", ownerTok)
	if code != 200 || hdr.Get("Content-Type") != "image/png" || hdr.Get("Content-Disposition") != "inline" ||
		hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Content-Security-Policy") != "sandbox; default-src 'none'" ||
		hdr.Get("Cache-Control") != "no-store" || hdr.Get("X-Attachment-SHA256") != pngAtt["sha256"] {
		t.Fatalf("unexpected thumbnail headers/status: code=%d hdr=%v", code, hdr)
	}
	decodedPNG, err := png.DecodeConfig(bytes.NewReader(thumbBytes))
	if err != nil || decodedPNG.Width != 300 || decodedPNG.Height != 200 {
		t.Fatalf("small PNG thumbnail was unexpectedly upscaled or corrupt: %v %v", decodedPNG, err)
	}
	if code, _, _ := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+pngID+"/thumbnail", memberTok); code != 404 {
		t.Fatalf("unlinked pending thumbnail leaked to another member: %d", code)
	}

	// 2. Opaque JPEG 1200x800 with EXIF orientation 6 (90° CW): display dimensions must swap to 800x1200,
	// thumbnail must downscale to 427x640 image/jpeg, and SECRET_EXIF_COMMENT must be stripped!
	exifJPEG := encodeJPEGWithExifOrientation(t, 1200, 800, 6)
	st, jpgResp := hc.upload(channel.ID, ownerTok, "photo.jpg", exifJPEG, "")
	if st != http.StatusCreated {
		t.Fatalf("jpeg upload failed: %d %v", st, jpgResp)
	}
	jpgAtt := jpgResp["attachment"].(map[string]any)
	jpgID := jpgAtt["id"].(string)
	jpgImg := jpgAtt["image"].(map[string]any)
	if int(jpgImg["width"].(float64)) != 800 || int(jpgImg["height"].(float64)) != 1200 {
		t.Fatalf("expected EXIF-swapped 800x1200 dimensions, got %v", jpgImg)
	}
	code, jpgHdr, jpgThumb := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+jpgID+"/thumbnail", ownerTok)
	if code != 200 || jpgHdr.Get("Content-Type") != "image/jpeg" || bytes.Contains(jpgThumb, []byte("SECRET_EXIF_COMMENT")) {
		t.Fatalf("jpeg thumbnail failed or leaked EXIF metadata: code=%d ct=%s", code, jpgHdr.Get("Content-Type"))
	}
	decodedJPG, err := jpeg.DecodeConfig(bytes.NewReader(jpgThumb))
	if err != nil || decodedJPG.Width != 427 || decodedJPG.Height != 640 {
		t.Fatalf("expected 427x640 oriented JPEG thumbnail, got %+v err=%v", decodedJPG, err)
	}

	// 3. GIF 800x400 -> downscaled to 640x320.
	pal := color.Palette{color.RGBA{R: 255, G: 255, B: 255, A: 255}, color.RGBA{R: 20, G: 20, B: 20, A: 255}}
	frame := image.NewPaletted(image.Rect(0, 0, 800, 400), pal)
	var gifBuf bytes.Buffer
	if err := gif.EncodeAll(&gifBuf, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{10}}); err != nil {
		t.Fatal(err)
	}
	st, gifResp := hc.upload(channel.ID, ownerTok, "anim.gif", gifBuf.Bytes(), "")
	if st != http.StatusCreated || gifResp["attachment"].(map[string]any)["image"] == nil {
		t.Fatalf("gif upload missing image metadata: %d %v", st, gifResp)
	}

	// 4. SVG, plain text, corrupt PNG, and oversize-header PNG (> 16384px) succeed as normal uploads WITHOUT image!
	var oversizePNG bytes.Buffer
	oversizePNG.Write([]byte("\x89PNG\r\n\x1a\n"))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 20000)
	binary.BigEndian.PutUint32(ihdr[4:8], 20000)
	ihdr[8] = 8
	ihdr[9] = 6
	_ = binary.Write(&oversizePNG, binary.BigEndian, uint32(13))
	oversizePNG.WriteString("IHDR")
	oversizePNG.Write(ihdr)
	oversizePNG.Write([]byte{0, 0, 0, 0})

	for _, nonThumb := range []struct {
		name string
		data []byte
	}{
		{"vector.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100"><rect width="100" height="100"/></svg>`)},
		{"notes.txt", []byte("plain text notes")},
		{"corrupt.png", []byte("\x89PNG\r\n\x1a\ncorrupt-payload")},
		{"huge.png", oversizePNG.Bytes()},
	} {
		st, up := hc.upload(channel.ID, ownerTok, nonThumb.name, nonThumb.data, "")
		if st != http.StatusCreated {
			t.Fatalf("%s upload should still succeed as ordinary file, got %d %v", nonThumb.name, st, up)
		}
		att := up["attachment"].(map[string]any)
		if _, hasImg := att["image"]; hasImg {
			t.Fatalf("%s must NOT have image metadata: %v", nonThumb.name, att)
		}
		if code, _, _ := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+att["id"].(string)+"/thumbnail", ownerTok); code != 404 {
			t.Fatalf("%s thumbnail must return 404, got %d", nonThumb.name, code)
		}
	}

	// 5. Link PNG + JPEG to a message: member can download thumbnail & content; platform-admin outsider gets 404; retraction deletes thumbnail!
	st, msgResp := hc.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", ownerTok, map[string]any{
		"clientMessageId": "img-msg-1",
		"body":            "Photos",
		"attachmentIds":   []string{pngID, jpgID},
	})
	if st != http.StatusCreated {
		t.Fatalf("send image message failed: %d %v", st, msgResp)
	}
	msgID := msgResp["message"].(map[string]any)["id"].(string)

	if code, _, _ := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+pngID+"/thumbnail", memberTok); code != 200 {
		t.Fatalf("member could not read linked thumbnail: %d", code)
	}
	if code, cHdr, _ := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+pngID+"/content", memberTok); code != 200 || cHdr.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(cHdr.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("/content endpoint lost forced attachment/octet-stream headers: %d %v", code, cHdr)
	}
	if code, _, _ := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+pngID+"/thumbnail", outsiderTok); code != 404 {
		t.Fatalf("platform-admin outsider bypassed thumbnail ACL: %d", code)
	}

	if st, _ := hc.json(http.MethodDelete, "/api/v1/conversations/"+channel.ID+"/messages/"+msgID, ownerTok, nil); st != 200 {
		t.Fatalf("retract image message failed: %d", st)
	}
	if code, _, _ := hc.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+pngID+"/thumbnail", memberTok); code != 404 {
		t.Fatalf("retracted message leaked thumbnail: %d", code)
	}
}
