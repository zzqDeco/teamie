package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"teamie/pkg/httpx"
	"teamie/services/chat/internal/domain"
)

func TestReactionVersionsPersistEmptyStateAndSkipNoopEvents(t *testing.T) {
	ctx := context.Background()
	store, _, conversation, owner, member := setup(t)
	message, _, err := store.Send(ctx, owner, conversation.ID, "versioned-message", "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	var originalUpdatedAt time.Time
	if err = store.DB.QueryRow(ctx, "SELECT updated_at FROM conversations WHERE id=$1", conversation.ID).Scan(&originalUpdatedAt); err != nil {
		t.Fatal(err)
	}
	api := store.App(nil, "")
	check := func(state domain.ReactionState, err error, version int64, count int) {
		t.Helper()
		if err != nil || state.ReactionsVersion != version || len(state.Reactions) != count {
			t.Fatalf("state=%#v err=%v want version=%d emojiCount=%d", state, err, version, count)
		}
	}
	state, err := api.AddReaction(ctx, owner.ID, conversation.ID, message.ID, "👍")
	check(state, err, 1, 1)
	state, err = api.AddReaction(ctx, owner.ID, conversation.ID, message.ID, "👍")
	check(state, err, 1, 1)
	state, err = api.RemoveReaction(ctx, owner.ID, conversation.ID, message.ID, "🎉")
	check(state, err, 1, 1)
	state, err = api.AddReaction(ctx, member.ID, conversation.ID, message.ID, "👍")
	check(state, err, 2, 1)
	if state.Reactions[0].Count != 2 {
		t.Fatalf("lost reactor: %#v", state)
	}
	state, err = api.RemoveReaction(ctx, owner.ID, conversation.ID, message.ID, "👍")
	check(state, err, 3, 1)
	state, err = api.RemoveReaction(ctx, member.ID, conversation.ID, message.ID, "👍")
	check(state, err, 4, 0)
	state, err = api.RemoveReaction(ctx, member.ID, conversation.ID, message.ID, "👍")
	check(state, err, 4, 0)
	fetched, err := api.Message(ctx, owner.ID, conversation.ID, message.ID)
	if err != nil || fetched.ReactionsVersion != 4 || len(fetched.Reactions) != 0 {
		t.Fatalf("empty message lost durable high-water mark: %#v %v", fetched, err)
	}
	events, _, _, err := api.Sync(ctx, owner.ID, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	changes := 0
	for _, event := range events {
		if event.Type == "reaction.updated" {
			changes++
			if event.ReactionsVersion != int64(changes) {
				t.Fatalf("non-monotonic event: %#v", event)
			}
		}
	}
	if changes != 4 {
		t.Fatalf("noop emitted durable events: %d", changes)
	}
	var lastSeq int64
	var updatedAt time.Time
	if err = store.DB.QueryRow(ctx, "SELECT last_seq,updated_at FROM conversations WHERE id=$1", conversation.ID).Scan(&lastSeq, &updatedAt); err != nil || lastSeq != 1 || !updatedAt.Equal(originalUpdatedAt) {
		t.Fatalf("reaction changed conversation ordering: seq=%d updatedAt=%v err=%v", lastSeq, updatedAt, err)
	}
}

func TestConcurrentReactionReadsReturnCoherentVersionAndList(t *testing.T) {
	ctx := context.Background()
	store, _, conversation, owner, _ := setup(t)
	message, _, err := store.Send(ctx, owner, conversation.ID, "coherent-message", "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	api := store.App(nil, "")
	var workers sync.WaitGroup
	errors := make(chan error, 2)
	start := make(chan struct{})
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 20; index++ {
			if _, err := api.AddReaction(ctx, owner.ID, conversation.ID, message.ID, "👍"); err != nil {
				errors <- err
				return
			}
			if _, err := api.RemoveReaction(ctx, owner.ID, conversation.ID, message.ID, "👍"); err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 80; index++ {
			current, err := api.Message(ctx, owner.ID, conversation.ID, message.ID)
			if err != nil {
				errors <- err
				return
			}
			if (current.ReactionsVersion%2 == 1) != (len(current.Reactions) == 1) {
				errors <- fmt.Errorf("torn reaction snapshot: %#v", current)
				return
			}
		}
	}()
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestConcurrentReactionMutationsConvergeToLatestCommittedState(t *testing.T) {
	ctx := context.Background()
	store, _, conversation, owner, member := setup(t)
	message, _, err := store.Send(ctx, owner, conversation.ID, "concurrent-message", "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	api := store.App(nil, "")
	type result struct {
		state domain.ReactionState
		err   error
	}
	results := make(chan result, 2)
	for _, actor := range []string{owner.ID, member.ID} {
		go func() {
			state, err := api.AddReaction(ctx, actor, conversation.ID, message.ID, "👍")
			results <- result{state, err}
		}()
	}
	for index := 0; index < 2; index++ {
		if result := <-results; result.err != nil {
			t.Fatal(result.err)
		}
	}
	current, err := api.Message(ctx, owner.ID, conversation.ID, message.ID)
	if err != nil || current.ReactionsVersion != 2 || len(current.Reactions) != 1 || current.Reactions[0].Count != 2 {
		t.Fatalf("concurrent adds lost state: %#v %v", current, err)
	}
	go func() {
		state, err := api.AddReaction(ctx, owner.ID, conversation.ID, message.ID, "👍")
		results <- result{state, err}
	}()
	go func() {
		state, err := api.RemoveReaction(ctx, owner.ID, conversation.ID, message.ID, "👍")
		results <- result{state, err}
	}()
	var latest domain.ReactionState
	for index := 0; index < 2; index++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.state.ReactionsVersion >= latest.ReactionsVersion {
			latest = result.state
		}
	}
	current, err = api.Message(ctx, owner.ID, conversation.ID, message.ID)
	if err != nil || current.ReactionsVersion != latest.ReactionsVersion || !reflect.DeepEqual(current.Reactions, latest.Reactions) {
		t.Fatalf("PUT/DELETE did not converge: current=%#v latest=%#v err=%v", current, latest, err)
	}
}

func TestRetractionResponseAndOldSyncEventsClearReactions(t *testing.T) {
	ctx := context.Background()
	store, _, conversation, owner, member := setup(t)
	message, _, err := store.Send(ctx, owner, conversation.ID, "retract-versioned-message", "has reactions", nil)
	if err != nil {
		t.Fatal(err)
	}
	api := store.App(nil, "")
	if _, err = api.AddReaction(ctx, member.ID, conversation.ID, message.ID, "👍"); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	origin := "https://teamie.example"
	identity := &fakeIdentity{users: map[string]httpx.User{token: owner}}
	server := httptest.NewServer((&Handler{Store: store, Identity: identity, Origin: origin}).Routes())
	defer server.Close()
	request, err := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/conversations/"+conversation.ID+"/messages/"+message.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Cookie", httpx.CookieName+"="+token)
	request.Header.Set("Origin", origin)
	request.Header.Set("X-CSRF-Token", "csrf")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Message domain.Message `json:"message"`
	}
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || body.Message.RetractedAt == nil || len(body.Message.Reactions) != 0 || body.Message.ReactionsVersion != 2 {
		t.Fatalf("retraction returned pre-delete state: status=%d message=%#v", response.StatusCode, body.Message)
	}
	events, _, _, err := api.Sync(ctx, owner.ID, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Message != nil && event.Message.ID == message.ID {
			if event.Message.RetractedAt == nil || event.Message.ReactionsVersion != 2 || len(event.Message.Reactions) != 0 {
				t.Fatalf("old message event kept pre-retraction state: %#v", event.Message)
			}
		}
		if event.Type == "reaction.updated" && (event.ReactionsVersion != 2 || len(event.Reactions) != 0) {
			t.Fatalf("redacted old reaction event kept a mismatched revision: %#v", event)
		}
	}
	if _, err = api.RemoveReaction(ctx, member.ID, conversation.ID, message.ID, "👍"); err != nil {
		t.Fatal(err)
	}
	fetched, err := api.Message(ctx, owner.ID, conversation.ID, message.ID)
	if err != nil || fetched.ReactionsVersion != 2 || len(fetched.Reactions) != 0 {
		t.Fatalf("idempotent removal changed retraction: %#v %v", fetched, err)
	}
}
