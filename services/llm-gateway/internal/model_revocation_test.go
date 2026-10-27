package gateway_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
)

type modelCallProbe struct {
	id  string
	ctx context.Context
}

type blockingModels struct{ started chan modelCallProbe }

type admissionCipher struct {
	Cipher
	entered chan struct{}
	release chan struct{}
}

func (c admissionCipher) Decrypt(data []byte) (string, error) {
	close(c.entered)
	<-c.release
	return c.Cipher.Decrypt(data)
}

func (p *blockingModels) wait(ctx context.Context, c ResolvedModel) error {
	p.started <- modelCallProbe{id: c.ID, ctx: ctx}
	<-ctx.Done()
	return domain.StreamError(ctx, "")
}
func (p *blockingModels) Chat(ctx context.Context, c ResolvedModel, _, _ string, _ ...int) (string, int64, int64, error) {
	return "", 0, 0, p.wait(ctx, c)
}
func (p *blockingModels) Stream(ctx context.Context, c ResolvedModel, _, _ string, _ func(string) error, _ ...int) (int64, int64, error) {
	return 0, 0, p.wait(ctx, c)
}

func TestModelProfileChangesCancelOnlyAffectedCalls(t *testing.T) {
	for _, mode := range []string{"unary", "stream"} {
		for _, change := range []string{"disable", "delete", "clear-key"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				crypto := testCipher(t)
				key, _ := crypto.Encrypt("fixture-key")
				store := &memoryStore{c: Config{
					Models: []ModelProfile{
						{ID: "a", BaseURL: "https://1.1.1.1/v1", Model: "model-a", CipherKey: key, Enabled: true},
						{ID: "b", BaseURL: "https://1.1.1.1/v1", Model: "model-b", CipherKey: key, Enabled: true},
					},
					Routing: ModelRoutingConfig{DefaultModelID: "a"},
				}}
				provider := &blockingModels{started: make(chan modelCallProbe, 3)}
				s := app.New(store, crypto, provider, nil, testToken)
				ctx, cancel := context.WithCancel(context.Background())
				var wg sync.WaitGroup
				defer func() { cancel(); wg.Wait() }()
				start := func(id, run string) {
					wg.Add(1)
					go func() {
						defer wg.Done()
						callCtx := app.WithRequestedModelID(ctx, id)
						if mode == "unary" {
							s.CallChat(callCtx, "owner", "agent", "session", run, "{}")
						} else {
							s.StreamChat(callCtx, "owner", "agent", "session", run, "", "{}", nil, func(StreamEvent) error { return nil })
						}
					}()
				}
				awaitCall := func() modelCallProbe {
					t.Helper()
					select {
					case call := <-provider.started:
						return call
					case <-time.After(3 * time.Second):
						t.Fatal("provider call did not start")
						return modelCallProbe{}
					}
				}
				start("a", "run-a")
				a := awaitCall()
				start("b", "run-b")
				b := awaitCall()
				if a.id != "a" || b.id != "b" {
					t.Fatal("selected model was not used")
				}
				webCtx, webDone := s.RegisterWebCancel(ctx, "agent", "web")
				defer webDone()
				var err error
				switch change {
				case "disable":
					no := false
					_, err = s.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "b", Enabled: &no})
				case "delete":
					_, err = s.DeleteModelProfile(ctx, "b")
				case "clear-key":
					_, err = s.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "b", ClearAPIKey: true})
				}
				if err != nil {
					t.Fatal(err)
				}
				if b.ctx.Err() == nil {
					t.Fatal("unavailable model B retained its active provider call while A stayed available")
				}
				if a.ctx.Err() != nil || webCtx.Err() != nil {
					t.Fatal("model B change canceled healthy model A or search")
				}
				// A new request for B must use healthy fallback A, not the revoked call's config.
				start("b", "run-fallback")
				fallback := awaitCall()
				if fallback.id != "a" || fallback.ctx.Err() != nil {
					t.Fatal("fallback request did not use healthy model A")
				}
				name := "Renamed A"
				if _, err := s.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "a", DisplayName: &name}); err != nil {
					t.Fatal(err)
				}
				if fallback.ctx.Err() != nil {
					t.Fatal("fallback call tracked unavailable requested B instead of resolved A")
				}
			})
		}
	}
}

func TestAdmissionErrorResponseDoesNotBlockModelChanges(t *testing.T) {
	s := app.New(&memoryStore{}, testCipher(t), nil, nil, testToken)
	emitting := make(chan StreamEvent, 1)
	release := make(chan struct{})
	var wg sync.WaitGroup
	defer func() { close(release); wg.Wait() }()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.StreamChat(context.Background(), "owner", "agent", "session", "run", "", "{}", nil, func(ev StreamEvent) error {
			emitting <- ev
			<-release // Simulate a client that has not read its error response yet.
			return nil
		})
	}()
	if ev := <-emitting; ev.ErrorCode != "model_unconfigured" {
		t.Fatal("expected an admission failure")
	}
	updated := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.DeleteModelProfile(context.Background(), "missing")
		updated <- err
	}()
	select {
	case err := <-updated:
		if domain.AsError(err).Code != "not_found" {
			t.Fatal("expected the missing-profile command to finish")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream error response held the model admission lock")
	}
}

func TestModelRevocationCannotMissAdmittingCall(t *testing.T) {
	for _, mode := range []string{"unary", "stream"} {
		t.Run(mode, func(t *testing.T) {
			crypto := testCipher(t)
			key, _ := crypto.Encrypt("fixture-key")
			store := &memoryStore{c: singleModel(ModelProfile{ID: "b", BaseURL: "https://1.1.1.1/v1", Model: "model-b", CipherKey: key, Enabled: true})}
			gate := admissionCipher{Cipher: crypto, entered: make(chan struct{}), release: make(chan struct{})}
			provider := &blockingModels{started: make(chan modelCallProbe, 1)}
			s := app.New(store, gate, provider, nil, testToken)
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			var release sync.Once
			defer func() {
				release.Do(func() { close(gate.release) })
				cancel()
				wg.Wait()
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				if mode == "unary" {
					s.CallChat(ctx, "owner", "agent", "session", "run", "{}")
				} else {
					s.StreamChat(ctx, "owner", "agent", "session", "run", "", "{}", nil, func(StreamEvent) error { return nil })
				}
			}()
			<-gate.entered // Configuration was read, but cancellation is not registered yet.
			updated := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				no := false
				_, err := s.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "b", Enabled: &no})
				updated <- err
			}()
			select {
			case err := <-updated:
				t.Fatalf("revocation passed an unregistered admission: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			release.Do(func() { close(gate.release) })
			select {
			case err := <-updated:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("configuration update waited for provider execution")
			}
			select {
			case call := <-provider.started:
				if call.ctx.Err() == nil {
					t.Fatal("revocation missed the admitting call")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("provider admission did not finish")
			}
		})
	}
}
