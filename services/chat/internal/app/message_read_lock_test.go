package app_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/httpx"
	"teamie/pkg/postgres"
	"teamie/pkg/postgres/testdb"
	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/domain"
	"teamie/services/chat/internal/infra/flags"
	"teamie/services/chat/internal/infra/pg"
)

type hookedConversationRepo struct {
	domain.ConversationRepository
	afterAccess func(ctx context.Context, conversationID, userID string)
}

func (h *hookedConversationRepo) ConversationAccess(ctx context.Context, conversationID, userID string) (domain.Conversation, error) {
	c, err := h.ConversationRepository.ConversationAccess(ctx, conversationID, userID)
	if err == nil && h.afterAccess != nil {
		h.afterAccess(ctx, conversationID, userID)
	}
	return c, err
}

func setupAppTest(t *testing.T) (*pgxpool.Pool, *pg.Repository, *app.Service, domain.Workspace, domain.Conversation, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	db, err := postgres.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = pg.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo := pg.New(postgres.New(db))
	svc := &app.Service{
		Tx:             repo,
		Mutations:      repo,
		Workspaces:     repo,
		Conversations:  repo,
		Memberships:    repo,
		Messages:       repo,
		Attachments:    repo,
		Outbox:         repo,
		Flags:          flags.New(httpx.Flags{}),
		SniffMediaType: http.DetectContentType,
	}
	owner := domain.User{ID: domain.NewID(), DisplayName: "Owner", Kind: "human"}
	member := domain.User{ID: domain.NewID(), DisplayName: "Member", Kind: "human"}
	ws, _, err := svc.CreateWorkspace(ctx, owner.ID, "Read lock workspace")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := svc.CreateInvite(ctx, owner.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = svc.AcceptInvite(ctx, member.ID, code); err != nil {
		t.Fatal(err)
	}
	group, err := svc.CreateConversation(ctx, owner.ID, ws.ID, "group", "Lock Group", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	return db, repo, svc, ws, group, owner, member
}

func waitForShareLockWaiter(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%SELECT id FROM conversations WHERE id=$1 FOR SHARE%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("conversation read did not wait on FOR SHARE lock")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForUpdateLockWaiter(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FOR UPDATE%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("conversation writer did not wait on FOR UPDATE lock")
		}
		time.Sleep(time.Millisecond)
	}
}

func requireNotFound(t *testing.T, err error) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Status != 404 {
		t.Fatalf("expected 404 not_found, got %v", err)
	}
}

type orderRecordingStub struct {
	domain.TxManager
	domain.WorkspaceRepository
	domain.ConversationRepository
	domain.MessageRepository
	steps []string
}

func (s *orderRecordingStub) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	s.steps = append(s.steps, "tx")
	return fn(ctx)
}

func (s *orderRecordingStub) GetConversationWorkspaceID(_ context.Context, _ string) (string, error) {
	s.steps = append(s.steps, "get_workspace_id")
	return "ws-1", nil
}

func (s *orderRecordingStub) WorkspaceLock(_ context.Context, _, _ string, _, _ bool) error {
	s.steps = append(s.steps, "workspace_lock")
	return nil
}

func (s *orderRecordingStub) LockConversationForShare(_ context.Context, _ string) error {
	s.steps = append(s.steps, "conversation_share_lock")
	return nil
}

func (s *orderRecordingStub) ConversationAccess(_ context.Context, conversationID, _ string) (domain.Conversation, error) {
	s.steps = append(s.steps, "conversation_access")
	return domain.Conversation{ID: conversationID, WorkspaceID: "ws-1", Type: "group"}, nil
}

func (s *orderRecordingStub) GetMessage(_ context.Context, conversationID, messageID string) (domain.Message, error) {
	s.steps = append(s.steps, "get_message")
	return domain.Message{ID: messageID, ConversationID: conversationID, Body: "ok"}, nil
}

func (s *orderRecordingStub) QueryMessageHistory(_ context.Context, _ string, _, _ int64, _ string, _ int, _ bool) ([]domain.Message, bool, error) {
	s.steps = append(s.steps, "query_history")
	return []domain.Message{{ID: "m-1", Body: "ok"}}, false, nil
}

func (s *orderRecordingStub) QueryReadReceipts(_ context.Context, c domain.Conversation, _ string) (domain.ReadReceipts, error) {
	s.steps = append(s.steps, "query_read_receipts")
	return domain.ReadReceipts{ConversationID: c.ID}, nil
}

func TestConversationReadsAcquireShareLockBeforeAccessAndQuery(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		run      func(svc *app.Service) error
		lastStep string
	}{
		{
			name: "message",
			run: func(svc *app.Service) error {
				_, err := svc.Message(ctx, "u-1", "c-1", "m-1")
				return err
			},
			lastStep: "get_message",
		},
		{
			name: "message_history",
			run: func(svc *app.Service) error {
				_, _, err := svc.MessageHistory(ctx, "u-1", "c-1", 0, 0, "", 20)
				return err
			},
			lastStep: "query_history",
		},
		{
			name: "file_history",
			run: func(svc *app.Service) error {
				_, _, err := svc.FileHistory(ctx, "u-1", "c-1", 0, "", 20)
				return err
			},
			lastStep: "query_history",
		},
		{
			name: "read_receipts",
			run: func(svc *app.Service) error {
				_, err := svc.ReadReceipts(ctx, "u-1", "c-1")
				return err
			},
			lastStep: "query_read_receipts",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &orderRecordingStub{}
			svc := &app.Service{
				Tx:            stub,
				Workspaces:    stub,
				Conversations: stub,
				Messages:      stub,
				Flags:         flags.New(httpx.Flags{}),
			}
			if err := tc.run(svc); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := []string{"tx", "get_workspace_id", "workspace_lock", "conversation_share_lock", "conversation_access", tc.lastStep}
			if !slices.Equal(stub.steps, want) {
				t.Fatalf("lock/query order = %v, want %v", stub.steps, want)
			}
		})
	}
}

func TestConversationReadsRecheckMembershipAfterWaitingForGroupMemberRemoval(t *testing.T) {
	for _, op := range []string{"message", "history", "files", "read_receipts"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			db, _, svc, _, group, owner, member := setupAppTest(t)

			att, _, err := svc.UploadAttachmentWithKey(ctx, owner.ID, group.ID, "secret.txt", []byte("classified"), "")
			if err != nil {
				t.Fatal(err)
			}
			msg, _, err := svc.SendAnnotated(ctx, owner, group.ID, domain.NewID(), "secret body", nil, []string{att.ID}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Read(ctx, member.ID, group.ID, msg.Seq); err != nil {
				t.Fatal(err)
			}

			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, "SELECT id FROM conversations WHERE id=$1 FOR UPDATE", group.ID); err != nil {
				t.Fatal(err)
			}

			errCh := make(chan error, 1)
			go func() {
				switch op {
				case "message":
					_, err := svc.Message(ctx, member.ID, group.ID, msg.ID)
					errCh <- err
				case "history":
					_, _, err := svc.MessageHistory(ctx, member.ID, group.ID, 0, 0, "", 50)
					errCh <- err
				case "files":
					_, _, err := svc.FileHistory(ctx, member.ID, group.ID, 0, "", 50)
					errCh <- err
				case "read_receipts":
					_, err := svc.ReadReceipts(ctx, member.ID, group.ID)
					errCh <- err
				}
			}()

			waitForShareLockWaiter(t, ctx, db)

			if _, err = tx.Exec(ctx, "DELETE FROM conversation_members WHERE conversation_id=$1 AND user_id=$2", group.ID, member.ID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			select {
			case readErr := <-errCh:
				requireNotFound(t, readErr)
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not finish after removal commit")
			}
		})
	}
}

func TestConversationReadsObserveCommittedRetractionAfterWaitingForLock(t *testing.T) {
	for _, op := range []string{"message", "history", "search_history", "files"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			db, _, svc, _, group, owner, member := setupAppTest(t)

			att, _, err := svc.UploadAttachmentWithKey(ctx, owner.ID, group.ID, "report.txt", []byte("confidential"), "")
			if err != nil {
				t.Fatal(err)
			}
			msg, _, err := svc.SendAnnotated(ctx, owner, group.ID, domain.NewID(), "retractable secret needle", nil, []string{att.ID}, nil)
			if err != nil {
				t.Fatal(err)
			}

			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, "SELECT id FROM conversations WHERE id=$1 FOR UPDATE", group.ID); err != nil {
				t.Fatal(err)
			}

			type result struct {
				msg  domain.Message
				list []domain.Message
				err  error
			}
			resCh := make(chan result, 1)
			go func() {
				switch op {
				case "message":
					m, err := svc.Message(ctx, member.ID, group.ID, msg.ID)
					resCh <- result{msg: m, err: err}
				case "history":
					ms, _, err := svc.MessageHistory(ctx, member.ID, group.ID, 0, 0, "", 50)
					resCh <- result{list: ms, err: err}
				case "search_history":
					ms, _, err := svc.MessageHistory(ctx, member.ID, group.ID, 0, 0, "needle", 50)
					resCh <- result{list: ms, err: err}
				case "files":
					ms, _, err := svc.FileHistory(ctx, member.ID, group.ID, 0, "", 50)
					resCh <- result{list: ms, err: err}
				}
			}()

			waitForShareLockWaiter(t, ctx, db)

			now := time.Now().UTC()
			if _, err = tx.Exec(ctx, "UPDATE messages SET body='',attachments='[]',mentions='[]',retracted_at=$2 WHERE id=$1", msg.ID, now); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, "DELETE FROM attachments WHERE message_id=$1", msg.ID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			select {
			case res := <-resCh:
				if res.err != nil {
					t.Fatalf("unexpected read error: %v", res.err)
				}
				switch op {
				case "message":
					if res.msg.Body != "" || res.msg.RetractedAt == nil || len(res.msg.Attachments) != 0 {
						t.Fatalf("expected retracted tombstone from Message(), got %+v", res.msg)
					}
				case "history":
					if len(res.list) != 1 || res.list[0].Body != "" || res.list[0].RetractedAt == nil || len(res.list[0].Attachments) != 0 {
						t.Fatalf("expected retracted tombstone in MessageHistory(), got %+v", res.list)
					}
				case "search_history", "files":
					if len(res.list) != 0 {
						t.Fatalf("retracted message matched %s: %+v", op, res.list)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not finish after retraction commit")
			}
		})
	}
}

func TestGroupMemberRemovalAndRetractionWaitForInFlightConversationReads(t *testing.T) {
	for _, writerKind := range []string{"remove_member", "retract_message"} {
		for _, readerKind := range []string{"message", "history", "read_receipts"} {
			if writerKind == "retract_message" && readerKind == "read_receipts" {
				continue
			}
			t.Run(writerKind+"_"+readerKind, func(t *testing.T) {
				ctx := context.Background()
				db, repo, svc, _, group, owner, member := setupAppTest(t)

				msg, _, err := svc.SendAnnotated(ctx, owner, group.ID, domain.NewID(), "in-flight body", nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}

				accessChecked := make(chan struct{})
				releaseReader := make(chan struct{})
				readerHooked := *svc
				readerHooked.Conversations = &hookedConversationRepo{
					ConversationRepository: repo,
					afterAccess: func(_ context.Context, conversationID, userID string) {
						if conversationID == group.ID && userID == member.ID {
							close(accessChecked)
							<-releaseReader
						}
					},
				}

				readDone := make(chan error, 1)
				go func() {
					switch readerKind {
					case "message":
						m, err := readerHooked.Message(ctx, member.ID, group.ID, msg.ID)
						if err == nil && m.Body != "in-flight body" {
							err = errors.New("unexpected message body during pre-writer read")
						}
						readDone <- err
					case "history":
						ms, _, err := readerHooked.MessageHistory(ctx, member.ID, group.ID, 0, 0, "", 50)
						if err == nil && (len(ms) != 1 || ms[0].Body != "in-flight body") {
							err = errors.New("unexpected history body during pre-writer read")
						}
						readDone <- err
					case "read_receipts":
						rr, err := readerHooked.ReadReceipts(ctx, member.ID, group.ID)
						if err == nil && rr.ConversationID != group.ID {
							err = errors.New("unexpected read receipts during pre-writer read")
						}
						readDone <- err
					}
				}()

				select {
				case <-accessChecked:
				case <-time.After(5 * time.Second):
					t.Fatal("reader did not reach ConversationAccess")
				}

				writerDone := make(chan error, 1)
				go func() {
					if writerKind == "remove_member" {
						_, err := svc.GroupMember(ctx, owner.ID, group.ID, member.ID, false)
						writerDone <- err
					} else {
						_, err := svc.ChangeAnnotatedMessage(ctx, owner.ID, group.ID, msg.ID, "", true, nil)
						writerDone <- err
					}
				}()

				waitForUpdateLockWaiter(t, ctx, db)

				select {
				case err := <-writerDone:
					t.Fatalf("writer committed while reader was between ConversationAccess and query: %v", err)
				default:
				}

				close(releaseReader)

				select {
				case err := <-readDone:
					if err != nil {
						t.Fatalf("in-flight reader failed: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("reader did not complete after release")
				}

				select {
				case err := <-writerDone:
					if err != nil {
						t.Fatalf("writer failed after reader completed: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("writer did not complete after reader released FOR SHARE lock")
				}

				// Subsequent reads after writer commit must observe the removal or retraction.
				if writerKind == "remove_member" {
					_, afterErr := svc.Message(ctx, member.ID, group.ID, msg.ID)
					requireNotFound(t, afterErr)
				} else {
					afterMsg, afterErr := svc.Message(ctx, member.ID, group.ID, msg.ID)
					if afterErr != nil || afterMsg.Body != "" || afterMsg.RetractedAt == nil {
						t.Fatalf("expected retracted message after writer commit, got %+v, err=%v", afterMsg, afterErr)
					}
				}
			})
		}
	}
}
