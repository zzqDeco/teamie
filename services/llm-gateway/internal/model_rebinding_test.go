package gateway_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"teamie/services/llm-gateway/internal/app"
)

func TestReboundModelRevokesOnlyItsAdmittedProvider(t *testing.T) {
	for _, mode := range []string{"unary", "stream"} {
		for _, change := range []string{"disable", "clear-key", "delete", "disable-destination"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				crypto := testCipher(t)
				key, err := crypto.Encrypt("review-fixture-key")
				if err != nil {
					t.Fatal(err)
				}
				store := &memoryStore{c: Config{
					Providers: []ProviderProfile{
						{ID: "pa", BaseURL: "https://1.1.1.1/v1", CipherKey: key, Enabled: true},
						{ID: "pb", BaseURL: "https://1.1.1.1/v1", CipherKey: key, Enabled: true},
					},
					Models:  []ModelProfile{{ID: "m", ProviderID: "pa", Model: "model-a", Enabled: true}},
					Routing: ModelRoutingConfig{DefaultModelID: "m"},
				}}
				provider := &blockingModels{started: make(chan modelCallProbe, 2)}
				s := app.New(store, crypto, provider, nil, testToken)
				ctx, cancel := context.WithCancel(context.Background())
				var wg sync.WaitGroup
				defer func() { cancel(); wg.Wait() }()
				startCall := func(run string) modelCallProbe {
					t.Helper()
					wg.Add(1)
					go func() {
						defer wg.Done()
						if mode == "unary" {
							s.CallChat(ctx, "owner", "agent", "session", run, "{}")
						} else {
							s.StreamChat(ctx, "owner", "agent", "session", run, "", "{}", nil, func(StreamEvent) error { return nil })
						}
					}()
					select {
					case call := <-provider.started:
						return call
					case <-time.After(3 * time.Second):
						t.Fatal("call never started")
						return modelCallProbe{}
					}
				}
				oldCall := startCall("run-old-provider")
				if oldCall.providerID != "pa" {
					t.Fatal("initial call did not use Provider A")
				}
				pb := "pb"
				if _, err := s.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "m", ProviderID: &pb}); err != nil {
					t.Fatal(err)
				}
				newCall := startCall("run-new-provider")
				if newCall.providerID != "pb" {
					t.Fatal("new call did not use Provider B")
				}
				webCtx, webDone := s.RegisterWebCancel(ctx, "agent", "web")
				defer webDone()
				switch change {
				case "disable-destination":
					no := false
					_, err = s.UpdateModelProvider(ctx, app.UpdateModelProviderInput{ID: "pb", Enabled: &no})
				case "disable":
					no := false
					_, err = s.UpdateModelProvider(ctx, app.UpdateModelProviderInput{ID: "pa", Enabled: &no})
				case "clear-key":
					_, err = s.UpdateModelProvider(ctx, app.UpdateModelProviderInput{ID: "pa", ClearAPIKey: true})
				case "delete":
					_, err = s.DeleteModelProvider(ctx, "pa")
				}
				if err != nil {
					t.Fatal(err)
				}
				if change == "disable-destination" {
					if oldCall.ctx.Err() != nil || newCall.ctx.Err() == nil {
						t.Fatal("destination revocation must cancel B's new call and preserve A's admitted call")
					}
				} else if oldCall.ctx.Err() == nil || newCall.ctx.Err() != nil {
					t.Fatalf("%s of A must cancel A's admitted call and preserve B's new call", change)
				}
				if webCtx.Err() != nil {
					t.Fatal("provider revocation canceled web search")
				}
			})
		}
	}
}
