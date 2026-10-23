package rpcx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/kitex/client/callopt"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/httpx"
)

type usersClient struct {
	identityservice.Client
	get func(context.Context, *im.UsersRequest) (*im.UsersResponse, error)
}

func (c usersClient) GetUsers(ctx context.Context, request *im.UsersRequest, _ ...callopt.Option) (*im.UsersResponse, error) {
	return c.get(ctx, request)
}

func TestIdentityUsersBatchesMergeWithoutDuplicateProfiles(t *testing.T) {
	for _, count := range []int{0, 1, 1000, 1001, 2001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ids := make([]string, count)
			for i := range ids {
				ids[i] = httpx.ID()
			}
			input := append([]string(nil), ids...)
			if len(ids) > 0 {
				input = append(input, ids[0], ids[len(ids)-1])
			}
			calls := 0
			requested := map[string]bool{}
			client := Identity{Client: usersClient{get: func(_ context.Context, request *im.UsersRequest) (*im.UsersResponse, error) {
				calls++
				if len(request.Ids) == 0 || len(request.Ids) > 1000 {
					t.Fatalf("unbounded identity RPC batch: %d", len(request.Ids))
				}
				users := []*im.User{}
				for i := len(request.Ids) - 1; i >= 0; i-- {
					id := request.Ids[i]
					if requested[id] {
						t.Fatal("identity lookup repeated an ID across batches")
					}
					requested[id] = true
					users = append(users, &im.User{Id: id, DisplayName: "Profile " + id, Kind: "human"})
				}
				return &im.UsersResponse{Users: users}, nil
			}}}
			profiles, err := client.Users(context.Background(), input)
			if err != nil || len(profiles) != count || calls != (count+999)/1000 {
				t.Fatal("identity batches lost profiles", len(profiles), calls, err)
			}
			seen := map[string]bool{}
			for _, user := range profiles {
				if !requested[user.ID] || seen[user.ID] || user.DisplayName != "Profile "+user.ID || user.Kind != "human" {
					t.Fatal("merged profiles were missing, duplicated, or altered", user)
				}
				seen[user.ID] = true
			}
			if profiles == nil {
				t.Fatal("empty result must remain a JSON array")
			}
		})
	}
}

func TestIdentityUsersLaterBatchFailureReturnsNoPartialProfiles(t *testing.T) {
	ids := make([]string, 1001)
	for i := range ids {
		ids[i] = httpx.ID()
	}
	for _, nilReply := range []bool{false, true} {
		t.Run(fmt.Sprint("nil-reply=", nilReply), func(t *testing.T) {
			calls := 0
			client := Identity{Client: usersClient{get: func(_ context.Context, request *im.UsersRequest) (*im.UsersResponse, error) {
				calls++
				if calls == 2 {
					if nilReply {
						return nil, nil
					}
					return nil, errors.New("fixture transport unavailable")
				}
				users := []*im.User{}
				for _, id := range request.Ids {
					users = append(users, &im.User{Id: id})
				}
				return &im.UsersResponse{Users: users}, nil
			}}}
			profiles, err := client.Users(context.Background(), ids)
			var failure *httpx.Error
			if profiles != nil || !errors.As(err, &failure) || failure.Status != 503 || failure.Code != "identity_unavailable" || calls != 2 {
				t.Fatal("later RPC failure returned partial or successful profiles", len(profiles), calls, err)
			}
		})
	}
}

type identityStub struct{ s *im.SessionResponse }

func (i identityStub) ValidateSession(context.Context, *im.SessionRequest, ...callopt.Option) (*im.SessionResponse, error) {
	return i.s, nil
}

func TestAdminAuthenticationAndCSRF(t *testing.T) {
	csrf := strings.Repeat("a", 64)
	session := &im.SessionResponse{Valid: true, User: &im.User{Id: "admin", Kind: "human", PlatformRole: "admin"}, CsrfToken: &csrf}
	auth := AdminAuth{Identity: identityStub{session}, Origin: "https://example.com"}
	request := func(method string) *http.Request {
		r := httptest.NewRequest(method, "https://example.com/api/v1/admin/model-gateway", nil)
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: strings.Repeat("b", 64)})
		return r
	}
	if _, e := auth.Require(httptest.NewRequest("GET", "/", nil)); e == nil {
		t.Fatal("anonymous accepted")
	}
	if _, e := auth.Require(request("GET")); e != nil {
		t.Fatal(e)
	}
	for _, method := range []string{"GET", "HEAD"} {
		r := request(method)
		r.Header.Set("X-CSRF-Token", "previous-browser-session")
		if _, e := auth.Require(r); e == nil || !strings.Contains(e.Error(), "Browser session changed") {
			t.Fatalf("changed browser session accepted: %v", e)
		}
		r.Header.Set("X-CSRF-Token", csrf)
		if _, e := auth.Require(r); e != nil {
			t.Fatal(e)
		}
	}
	session.User.PlatformRole = "user"
	if _, e := auth.Require(request("GET")); e == nil {
		t.Fatal("member accepted")
	}
	session.User.PlatformRole = "admin"
	session.User.Kind = "agent"
	if _, e := auth.Require(request("GET")); e == nil {
		t.Fatal("agent principal accepted")
	}
	session.User.Kind = "human"
	r := request("PUT")
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-CSRF-Token", csrf)
	if _, e := auth.Require(r); e == nil {
		t.Fatal("wrong origin accepted")
	}
	r.Header.Set("Origin", auth.Origin)
	r.Header.Del("X-CSRF-Token")
	if _, e := auth.Require(r); e == nil {
		t.Fatal("missing CSRF accepted")
	}
	r.Header.Set("X-CSRF-Token", csrf)
	if _, e := auth.Require(r); e != nil {
		t.Fatal(e)
	}
}
