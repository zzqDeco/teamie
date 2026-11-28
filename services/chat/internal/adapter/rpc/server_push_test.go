package rpc_test

import (
	"context"
	"testing"

	"teamie/contracts/gen/im/kitex_gen/im"
	rpcadapter "teamie/services/chat/internal/adapter/rpc"
	"teamie/services/chat/internal/app"
)

func TestEvaluatePushRecipients_TokenAuth(t *testing.T) {
	srv := &rpcadapter.Server{
		App: &app.Service{
			PushToken: "0123456789abcdef0123456789abcdef",
		},
	}
	resp, err := srv.EvaluatePushRecipients(context.Background(), &im.PushEvaluateRequest{
		ServiceToken:     "wrong-token",
		ConversationId:   "c1",
		MessageId:        "m1",
		CandidateUserIds: []string{"u1"},
	})
	if err != nil {
		t.Fatalf("unexpected rpc error: %v", err)
	}
	if resp.Ok || resp.ErrorCode != "forbidden" {
		t.Fatalf("expected forbidden for invalid push token, got %+v", resp)
	}
}
