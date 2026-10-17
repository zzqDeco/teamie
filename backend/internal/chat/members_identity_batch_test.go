package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/kitex/client/callopt"
	"teamie/backend/internal/api"
	"teamie/backend/internal/rpcutil"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/kitex_gen/im/identityservice"
)

type workspaceProfilesClient struct {
	identityservice.Client
	owner api.User
	calls [][]string
}

func (c *workspaceProfilesClient) ValidateSession(context.Context, *im.SessionRequest, ...callopt.Option) (*im.SessionResponse, error) {
	return &im.SessionResponse{Valid: true, User: rpcutil.ToUser(c.owner)}, nil
}

func (c *workspaceProfilesClient) GetUsers(_ context.Context, request *im.UsersRequest, _ ...callopt.Option) (*im.UsersResponse, error) {
	c.calls = append(c.calls, append([]string(nil), request.Ids...))
	if len(request.Ids) > 1000 {
		return nil, fmt.Errorf("identity rejects more than 1000 IDs")
	}
	profiles := []*im.User{}
	for _, id := range request.Ids {
		profiles = append(profiles, &im.User{Id: id, DisplayName: "Member " + id, Kind: "human"})
	}
	return &im.UsersResponse{Users: profiles}, nil
}

func TestWorkspaceMemberListResolves1001StoredMembers(t *testing.T) {
	ctx := context.Background()
	s, ws, _, owner, member := setup(t)
	ids := []string{owner.ID, member.ID}
	for i := 0; i < 999; i++ {
		ids = append(ids, api.ID())
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role)
		SELECT $1,id,'member' FROM unnest($2::text[]) id ON CONFLICT DO NOTHING`, ws.ID, ids); err != nil {
		t.Fatal(err)
	}
	profiles := &workspaceProfilesClient{owner: owner}
	handler := (&Handler{Store: s, Identity: &rpcutil.Identity{Client: profiles}, Origin: "http://teamie.test"}).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/members", nil)
	request.AddCookie(&http.Cookie{Name: "teamie_session", Value: strings.Repeat("a", 64)})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal("large workspace member list failed", response.Code, response.Body.String())
	}
	var body struct {
		Members []Member `json:"members"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Members) != 1001 || len(profiles.calls) != 2 || len(profiles.calls[0]) != 1000 || len(profiles.calls[1]) != 1 {
		t.Fatal("member handler did not use complete bounded identity batches", len(body.Members), len(profiles.calls), err)
	}
	expected := map[string]bool{}
	for _, id := range ids {
		expected[id] = true
	}
	for _, member := range body.Members {
		if !expected[member.UserID] || member.DisplayName != "Member "+member.UserID || member.Kind != "human" {
			t.Fatal("resolved member list omitted or mismatched profiles", member)
		}
		delete(expected, member.UserID)
	}
	if len(expected) != 0 {
		t.Fatal("stored workspace members were silently omitted")
	}
}
