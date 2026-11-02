package marketplace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"teamie/pkg/httpx"
	"teamie/pkg/postgres"
	"teamie/pkg/postgres/testdb"
	"teamie/services/marketplace/internal/app"
	"teamie/services/marketplace/internal/domain"
	"teamie/services/marketplace/internal/infra/pg"
)

type stubEndpointVerifier struct {
	fn func(ctx context.Context, in domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error)
}

func (s *stubEndpointVerifier) VerifyEndpoints(ctx context.Context, in domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error) {
	return s.fn(ctx, in)
}

func setupProbeAdmissionService(t *testing.T) (*app.Service, domain.User) {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	dev := httpx.User{
		ID:           domain.NewID(),
		Email:        "probe-dev@example.com",
		DisplayName:  "Probe Dev",
		Kind:         "human",
		PlatformRole: "member",
		CreatedAt:    time.Now().UTC(),
	}
	idMock := &fakeIdentity{
		users: map[string]httpx.User{dev.ID: dev},
	}
	ff := &toggleFlags{}
	ff.enabled.Store(true)
	reqHTTPS := false
	repo := pg.New(postgres.New(pool))

	svc := &app.Service{
		Tx:                  repo,
		Marketplace:         repo,
		Identity:            domainIdentityFromFake{idMock},
		Chat:                &fakeChatScope{},
		Flags:               ff,
		AppOrigin:           "http://teamie.local:8080",
		RequirePublicHTTPS:  &reqHTTPS,
		VerifyCooldown:      2 * time.Second,
		MaxConcurrentProbes: 8,
	}
	return svc, domain.User{
		ID:           dev.ID,
		Email:        dev.Email,
		DisplayName:  dev.DisplayName,
		Kind:         dev.Kind,
		PlatformRole: dev.PlatformRole,
	}
}

func assertRateLimitedError(t *testing.T, err error) {
	t.Helper()
	var domErr *domain.Error
	if !errors.As(err, &domErr) || domErr.Status != 429 || domErr.Code != "marketplace_verify_rate_limited" {
		t.Fatalf("expected 429 marketplace_verify_rate_limited, got %v", err)
	}
}

func TestConcurrentSubmitAndVerifySingleFlight(t *testing.T) {
	ctx := context.Background()
	svc, dev := setupProbeAdmissionService(t)

	var (
		probeCalls atomic.Int32
		entered    = make(chan struct{}, 16)
		releaseCh  = make(chan struct{})
	)
	svc.Verifier = &stubEndpointVerifier{
		fn: func(_ context.Context, _ domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error) {
			probeCalls.Add(1)
			entered <- struct{}{}
			<-releaseCh
			return domain.MarketplaceEndpointVerification{
				OK:         true,
				VerifiedAt: svc.Now(),
			}, nil
		},
	}
	nowMu := sync.Mutex{}
	curNow := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return curNow
	}

	appA, _, err := svc.CreateDeveloperMarketplaceApp(ctx, dev, domain.CreateMarketplaceAppInput{
		Slug:        "singleflight-submit",
		Name:        "Singleflight Submit",
		Description: "Concurrent submit test",
		WebURL:      "http://ext.example.com/ui",
		MCPURL:      "http://ext.example.com/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Subcase A: 6 concurrent Submit calls on the same appID -> 1 probes & succeeds, 5 immediately return 429.
	const workers = 6
	errs := make(chan error, workers)
	go func() {
		_, _, subErr := svc.SubmitDeveloperMarketplaceApp(ctx, dev, appA.ID, "first")
		errs <- subErr
	}()
	<-entered

	var wg sync.WaitGroup
	for i := 1; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, subErr := svc.SubmitDeveloperMarketplaceApp(ctx, dev, appA.ID, fmt.Sprintf("dup-%d", idx))
			errs <- subErr
		}(i)
	}
	wg.Wait()
	close(releaseCh)

	var okCount, rateLimitedCount int
	for i := 0; i < workers; i++ {
		e := <-errs
		if e == nil {
			okCount++
		} else {
			assertRateLimitedError(t, e)
			rateLimitedCount++
		}
	}
	if okCount != 1 || rateLimitedCount != workers-1 || probeCalls.Load() != 1 {
		t.Fatalf("expected 1 ok, %d rate-limited, 1 underlying probe; got ok=%d rl=%d probes=%d", workers-1, okCount, rateLimitedCount, probeCalls.Load())
	}

	// Subcase B: Verify in flight blocks concurrent Submit on the same appID; once Verify finishes, immediate Submit succeeds.
	appB, _, err := svc.CreateDeveloperMarketplaceApp(ctx, dev, domain.CreateMarketplaceAppInput{
		Slug:        "singleflight-mixed",
		Name:        "Singleflight Mixed",
		Description: "Verify + Submit concurrency test",
		WebURL:      "http://ext.example.com/ui",
		MCPURL:      "http://ext.example.com/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}

	enteredB := make(chan struct{}, 4)
	releaseB := make(chan struct{})
	var callsB atomic.Int32
	svc.Verifier = &stubEndpointVerifier{
		fn: func(_ context.Context, _ domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error) {
			n := callsB.Add(1)
			if n == 1 {
				enteredB <- struct{}{}
				<-releaseB
			}
			return domain.MarketplaceEndpointVerification{
				OK:         true,
				VerifiedAt: svc.Now(),
			}, nil
		},
	}

	verifyDone := make(chan error, 1)
	go func() {
		_, _, vErr := svc.VerifyDeveloperMarketplaceApp(ctx, dev, appB.ID)
		verifyDone <- vErr
	}()
	<-enteredB

	// Concurrent Submit while Verify is in flight must return 429 immediately without invoking VerifyEndpoints.
	if _, _, subErr := svc.SubmitDeveloperMarketplaceApp(ctx, dev, appB.ID, "concurrent"); subErr == nil {
		t.Fatal("expected concurrent Submit during in-flight Verify to be rate limited")
	} else {
		assertRateLimitedError(t, subErr)
	}
	if callsB.Load() != 1 {
		t.Fatalf("expected VerifyEndpoints call count 1 while Verify in flight, got %d", callsB.Load())
	}

	close(releaseB)
	if vErr := <-verifyDone; vErr != nil {
		t.Fatalf("expected in-flight Verify to complete cleanly, got %v", vErr)
	}

	// Immediately after successful Verify (0s elapsed < 2s VerifyCooldown), Submit succeeds cleanly.
	submitted, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, appB.ID, "after-verify")
	if err != nil {
		t.Fatalf("expected immediate Submit after successful Verify to succeed, got %v", err)
	}
	if submitted.ReviewStatus != domain.ReviewStatusPending || callsB.Load() != 2 {
		t.Fatalf("expected pending_review and 2 total probes, got status=%s probes=%d", submitted.ReviewStatus, callsB.Load())
	}
}

func TestVerifyThenImmediateSubmitAllowedAndFailedProbeCooldownEnforced(t *testing.T) {
	ctx := context.Background()
	svc, dev := setupProbeAdmissionService(t)

	var nowMu sync.Mutex
	curNow := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) {
		nowMu.Lock()
		curNow = curNow.Add(d)
		nowMu.Unlock()
	}
	svc.Now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return curNow
	}

	var probeOK atomic.Bool
	probeOK.Store(true)
	svc.Verifier = &stubEndpointVerifier{
		fn: func(_ context.Context, _ domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error) {
			return domain.MarketplaceEndpointVerification{
				OK:         probeOK.Load(),
				VerifiedAt: svc.Now(),
			}, nil
		},
	}

	created, _, err := svc.CreateDeveloperMarketplaceApp(ctx, dev, domain.CreateMarketplaceAppInput{
		Slug:        "cooldown-flow",
		Name:        "Cooldown Flow",
		Description: "Cooldown policy verification",
		WebURL:      "http://ext.example.com/ui",
		MCPURL:      "http://ext.example.com/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Verify succeeds -> 50ms later second Verify gets 429, but immediate Submit succeeds!
	if _, rep, err := svc.VerifyDeveloperMarketplaceApp(ctx, dev, created.ID); err != nil || !rep.OK {
		t.Fatalf("expected initial Verify success, got rep=%+v err=%v", rep, err)
	}
	advance(50 * time.Millisecond)
	if _, _, err := svc.VerifyDeveloperMarketplaceApp(ctx, dev, created.ID); err == nil {
		t.Fatal("expected back-to-back Verify within cooldown to return 429")
	} else {
		assertRateLimitedError(t, err)
	}
	submitted, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, created.ID, "v1")
	if err != nil || submitted.ReviewStatus != domain.ReviewStatusPending {
		t.Fatalf("expected immediate Submit after successful Verify to succeed, got app=%+v err=%v", submitted, err)
	}

	// 2. Withdraw back to draft, swap to a failing endpoint, and verify failed Submit enforces per-app cooldown
	// even if developer mutates DraftWebURL query parameters.
	if _, _, err := svc.WithdrawDeveloperMarketplaceApp(ctx, dev, created.ID); err != nil {
		t.Fatal(err)
	}
	probeOK.Store(false)
	if _, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, created.ID, "broken"); err == nil {
		t.Fatal("expected Submit against broken endpoint to fail")
	}
	advance(50 * time.Millisecond)

	bypassWeb := "http://ext.example.com/ui?cachebust=1"
	if _, err := svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{
		WebURL: &bypassWeb,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, created.ID, "retry-too-fast"); err == nil {
		t.Fatal("expected repeated Submit after failure within cooldown to return 429")
	} else {
		assertRateLimitedError(t, err)
	}
	if _, _, err := svc.VerifyDeveloperMarketplaceApp(ctx, dev, created.ID); err == nil {
		t.Fatal("expected Verify immediately after failed Submit within cooldown to return 429")
	} else {
		assertRateLimitedError(t, err)
	}

	// 3. Advance past 2s cooldown, fix endpoint -> Verify succeeds -> immediate Submit succeeds.
	advance(2100 * time.Millisecond)
	probeOK.Store(true)
	if _, rep, err := svc.VerifyDeveloperMarketplaceApp(ctx, dev, created.ID); err != nil || !rep.OK {
		t.Fatalf("expected Verify after cooldown expiry to succeed, got rep=%+v err=%v", rep, err)
	}
	advance(20 * time.Millisecond)
	if resubmitted, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, created.ID, "fixed"); err != nil || resubmitted.ReviewStatus != domain.ReviewStatusPending {
		t.Fatalf("expected immediate Submit after post-recovery Verify to succeed, got app=%+v err=%v", resubmitted, err)
	}
}

func TestGlobalVerifyProbeConcurrencyCapAndReleaseSafety(t *testing.T) {
	ctx := context.Background()
	svc, dev := setupProbeAdmissionService(t)
	svc.MaxConcurrentProbes = 3

	var apps []domain.MarketplaceApp
	for i := 0; i < 5; i++ {
		created, _, err := svc.CreateDeveloperMarketplaceApp(ctx, dev, domain.CreateMarketplaceAppInput{
			Slug:        fmt.Sprintf("cap-app-%d", i),
			Name:        fmt.Sprintf("Cap App %d", i),
			Description: "Global concurrency cap test",
			WebURL:      "http://ext.example.com/ui",
			MCPURL:      "http://ext.example.com/mcp",
		})
		if err != nil {
			t.Fatal(err)
		}
		apps = append(apps, created)
	}

	entered := make(chan string, 8)
	releaseBySlug := map[string]chan string{}
	for _, a := range apps {
		releaseBySlug[a.Slug] = make(chan string, 1)
	}

	svc.Verifier = &stubEndpointVerifier{
		fn: func(callCtx context.Context, in domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error) {
			entered <- in.Slug
			select {
			case <-callCtx.Done():
				return domain.MarketplaceEndpointVerification{}, callCtx.Err()
			case action := <-releaseBySlug[in.Slug]:
				if action == "panic" {
					panic("simulated verifier panic")
				}
				return domain.MarketplaceEndpointVerification{
					OK:         true,
					VerifiedAt: time.Now().UTC(),
				}, nil
			}
		},
	}

	ctxCancel, cancel := context.WithCancel(ctx)
	defer cancel()

	done0 := make(chan error, 1)
	go func() {
		_, _, err := svc.VerifyDeveloperMarketplaceApp(ctxCancel, dev, apps[0].ID)
		done0 <- err
	}()
	done1 := make(chan any, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done1 <- r
				return
			}
			done1 <- nil
		}()
		_, _, _ = svc.SubmitDeveloperMarketplaceApp(ctx, dev, apps[1].ID, "panic-case")
	}()
	done2 := make(chan error, 1)
	go func() {
		_, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, apps[2].ID, "ok-case")
		done2 <- err
	}()

	for i := 0; i < 3; i++ {
		<-entered
	}

	// Slots 1..3 are occupied; app 3 (Submit) and app 4 (Verify) must fail fast with 429.
	if _, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, apps[3].ID, "overflow"); err == nil {
		t.Fatal("expected global probe cap overflow on Submit to return 429")
	} else {
		assertRateLimitedError(t, err)
	}
	if _, _, err := svc.VerifyDeveloperMarketplaceApp(ctx, dev, apps[4].ID); err == nil {
		t.Fatal("expected global probe cap overflow on Verify to return 429")
	} else {
		assertRateLimitedError(t, err)
	}

	// Cancel app[0] and trigger panic on app[1]; both slots must be released cleanly.
	cancel()
	if err := <-done0; err == nil {
		t.Fatal("expected canceled Verify to return error")
	}
	releaseBySlug[apps[1].Slug] <- "panic"
	if recovered := <-done1; recovered == nil {
		t.Fatal("expected simulated panic to be recovered")
	}

	// Now 2 slots are free again -> apps[3] and apps[4] are admitted and succeed!
	releaseBySlug[apps[3].Slug] <- "ok"
	releaseBySlug[apps[4].Slug] <- "ok"
	if sub3, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, apps[3].ID, "admitted-after-release"); err != nil || sub3.ReviewStatus != domain.ReviewStatusPending {
		t.Fatalf("expected apps[3] Submit to succeed after slot release, got app=%+v err=%v", sub3, err)
	}
	<-entered
	if _, rep4, err := svc.VerifyDeveloperMarketplaceApp(ctx, dev, apps[4].ID); err != nil || !rep4.OK {
		t.Fatalf("expected apps[4] Verify to succeed after slot release, got rep=%+v err=%v", rep4, err)
	}
	<-entered

	releaseBySlug[apps[2].Slug] <- "ok"
	if err := <-done2; err != nil {
		t.Fatalf("expected apps[2] Submit to finish cleanly, got %v", err)
	}
}
