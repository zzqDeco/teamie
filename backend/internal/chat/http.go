package chat

import (
	"net/http"
	"strconv"
	"teamie/backend/internal/api"
	"teamie/backend/pkg/imlimits"
)

type Handler struct {
	Store    *Store
	Identity api.Identity
	Origin   string
}

func queryInt(r *http.Request, key string, fallback, min, max int64) (int64, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < min || n > max {
		return 0, api.Err(400, "invalid_query", "Invalid "+key)
	}
	return n, nil
}
func (h *Handler) Routes() http.Handler {
	m := http.NewServeMux()
	api.Operations(m, func(r *http.Request) error {
		if e := h.Store.Ping(r.Context()); e != nil {
			return e
		}
		return h.Identity.Ping(r.Context())
	})
	auth := func(pattern string, next api.AuthedHandler) {
		m.HandleFunc(pattern, api.Auth(h.Identity, h.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
			w.Header().Set("Cache-Control", "no-store")
			return next(w, r, s)
		}))
	}
	h.attachmentRoutes(auth)
	auth("GET /api/v1/bootstrap", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		ws, e := h.Store.Workspaces(r.Context(), s.User.ID)
		if e != nil {
			return e
		}
		cs, e := h.Store.Conversations(r.Context(), s.User.ID, "")
		if e != nil {
			return e
		}
		head, e := h.Store.Head(r.Context(), s.User.ID)
		if e != nil {
			return e
		}
		flags := h.Store.Flags.Load()
		owner := false
		for _, item := range ws {
			if item.CreatedBy == s.User.ID {
				owner = true
			}
		}
		flags.Flags["im.channel.create"] = flags.Flags["im.channel.create"] && owner
		api.Write(w, 200, map[string]any{"user": s.User, "workspaces": ws, "conversations": cs, "capabilities": flags.Flags, "flagsRevision": flags.Revision, "syncCursor": head, "attachmentLimits": map[string]int{"maxFileBytes": imlimits.AttachmentBytes, "maxFilesPerMessage": imlimits.AttachmentsPerMessage}})
		return nil
	})
	auth("POST /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		if s.User.Kind != "human" {
			return api.Err(403, "forbidden", "A human manages workspace membership")
		}
		var in struct {
			Name string `json:"name"`
		}
		if e := api.Decode(w, r, &in); e != nil {
			return e
		}
		ws, c, e := h.Store.CreateWorkspace(r.Context(), s.User.ID, in.Name)
		if e != nil {
			return e
		}
		api.Write(w, 201, map[string]any{"workspace": ws, "conversation": c})
		return nil
	})
	auth("GET /api/v1/workspaces/{id}/members", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		members, e := h.Store.Members(r.Context(), s.User.ID, r.PathValue("id"))
		if e != nil {
			return e
		}
		ids := []string{}
		for _, m := range members {
			ids = append(ids, m.UserID)
		}
		users, e := h.Identity.Users(r.Context(), ids)
		if e != nil {
			return e
		}
		names := map[string]api.User{}
		for _, u := range users {
			names[u.ID] = u
		}
		for i := range members {
			if u, ok := names[members[i].UserID]; ok {
				members[i].DisplayName = u.DisplayName
				members[i].Kind = u.Kind
			}
		}
		api.Write(w, 200, map[string]any{"members": members})
		return nil
	})
	auth("POST /api/v1/workspaces/{id}/invites", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		code, expiry, e := h.Store.CreateInvite(r.Context(), s.User.ID, r.PathValue("id"))
		if e != nil {
			return e
		}
		api.Write(w, 201, map[string]any{"code": code, "expiresAt": expiry, "url": "/join/" + code})
		return nil
	})
	m.HandleFunc("GET /api/v1/invites/{code}", func(w http.ResponseWriter, r *http.Request) {
		ws, expiry, e := h.Store.Invite(r.Context(), r.PathValue("code"))
		if e != nil {
			api.Fail(w, e)
			return
		}
		api.Write(w, 200, map[string]any{"workspace": map[string]string{"id": ws.ID, "name": ws.Name}, "expiresAt": expiry})
	})
	auth("POST /api/v1/invites/{code}/accept", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		if s.User.Kind != "human" {
			return api.Err(403, "forbidden", "A human manages workspace membership")
		}
		ws, cs, e := h.Store.AcceptInvite(r.Context(), s.User.ID, r.PathValue("code"))
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"workspace": ws, "conversations": cs})
		return nil
	})
	auth("DELETE /api/v1/workspaces/{id}/members/{userId}", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		if e := h.Store.RemoveMember(r.Context(), s.User.ID, r.PathValue("id"), r.PathValue("userId")); e != nil {
			return e
		}
		api.Write(w, 204, nil)
		return nil
	})
	auth("GET /api/v1/workspaces/{id}/conversations", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		cs, e := h.Store.Conversations(r.Context(), s.User.ID, r.PathValue("id"))
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"conversations": cs})
		return nil
	})
	auth("POST /api/v1/workspaces/{id}/conversations", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		var in struct {
			Type      string   `json:"type"`
			Name      string   `json:"name"`
			MemberIDs []string `json:"memberIds"`
		}
		if e := api.Decode(w, r, &in); e != nil {
			return e
		}
		c, e := h.Store.CreateConversation(r.Context(), s.User.ID, r.PathValue("id"), in.Type, in.Name, in.MemberIDs)
		if e != nil {
			return e
		}
		api.Write(w, 201, map[string]any{"conversation": c})
		return nil
	})
	auth("GET /api/v1/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		before, e := queryInt(r, "beforeSeq", 0, 0, 1<<62)
		if e != nil {
			return e
		}
		limit, e := queryInt(r, "limit", 50, 1, 100)
		if e != nil {
			return e
		}
		around, e := queryInt(r, "aroundSeq", 0, 0, 1<<62)
		if e != nil {
			return e
		}
		ms, more, e := h.Store.MessageHistory(r.Context(), s.User.ID, r.PathValue("id"), before, around, r.URL.Query().Get("q"), int(limit))
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"messages": ms, "hasMore": more})
		return nil
	})
	auth("GET /api/v1/conversations/{id}/messages/{messageId}", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		m, e := h.Store.Message(r.Context(), s.User.ID, r.PathValue("id"), r.PathValue("messageId"))
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"message": m})
		return nil
	})
	auth("POST /api/v1/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		var in struct {
			ClientMessageID string   `json:"clientMessageId"`
			Body            string   `json:"body"`
			ReplyToID       *string  `json:"replyToId"`
			AttachmentIDs   []string `json:"attachmentIds"`
		}
		if e := api.Decode(w, r, &in); e != nil {
			return e
		}
		msg, created, e := h.Store.SendWithAttachments(r.Context(), s.User, r.PathValue("id"), in.ClientMessageID, in.Body, in.ReplyToID, in.AttachmentIDs)
		if e != nil {
			return e
		}
		status := 200
		if created {
			status = 201
		}
		api.Write(w, status, map[string]any{"message": msg})
		return nil
	})
	change := func(retract bool) api.AuthedHandler {
		return func(w http.ResponseWriter, r *http.Request, s api.Session) error {
			var in struct {
				Body string `json:"body"`
			}
			if !retract {
				if e := api.Decode(w, r, &in); e != nil {
					return e
				}
			}
			msg, e := h.Store.ChangeMessage(r.Context(), s.User.ID, r.PathValue("id"), r.PathValue("messageId"), in.Body, retract)
			if e != nil {
				return e
			}
			api.Write(w, 200, map[string]any{"message": msg})
			return nil
		}
	}
	auth("PATCH /api/v1/conversations/{id}/messages/{messageId}", change(false))
	auth("DELETE /api/v1/conversations/{id}/messages/{messageId}", change(true))
	auth("POST /api/v1/conversations/{id}/read", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		var in struct {
			Seq int64 `json:"seq"`
		}
		if e := api.Decode(w, r, &in); e != nil {
			return e
		}
		seq, e := h.Store.Read(r.Context(), s.User.ID, r.PathValue("id"), in.Seq)
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"readSeq": seq})
		return nil
	})
	auth("GET /api/v1/sync", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		limit, e := queryInt(r, "limit", 100, 1, 500)
		if e != nil {
			return e
		}
		events, cursor, more, e := h.Store.Sync(r.Context(), s.User.ID, r.URL.Query().Get("cursor"), int(limit))
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"events": events, "cursor": cursor, "hasMore": more})
		return nil
	})
	auth("POST /api/v1/conversations/{id}/members", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		var in struct {
			UserID string `json:"userId"`
		}
		if e := api.Decode(w, r, &in); e != nil {
			return e
		}
		c, e := h.Store.GroupMember(r.Context(), s.User.ID, r.PathValue("id"), in.UserID, true)
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"conversation": c})
		return nil
	})
	auth("DELETE /api/v1/conversations/{id}/members/{userId}", func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		c, e := h.Store.GroupMember(r.Context(), s.User.ID, r.PathValue("id"), r.PathValue("userId"), false)
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"conversation": c})
		return nil
	})
	return m
}
