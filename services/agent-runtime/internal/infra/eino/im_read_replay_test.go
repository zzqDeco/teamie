package eino

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAuthorizedTombstoneReceiptReplaysExactly(t *testing.T) {
	for _, name := range []string{"im_get_message", "im_list_messages"} {
		t.Run(name, func(t *testing.T) {
			f := newContextArchiveFixture(t)
			message := contextAddForeignMessage(f, "")
			stamp := "2026-10-28T00:00:00Z"
			message.RetractedAt = &stamp
			f.mu.Lock()
			f.messages[message.ID] = message
			f.mu.Unlock()
			var args any = getMessageArgs{ConversationID: message.ConversationID, MessageID: message.ID}
			if name == "im_list_messages" {
				args = listMessagesArgs{ConversationID: message.ConversationID}
			}
			first, err := invokeContextTool(contextRealIMNode(t, f), f.ctx(), "already-tombstoned", name, args)
			if err != nil || !strings.Contains(first, `"ok":true`) || !strings.Contains(first, `"retractedAt"`) {
				t.Fatalf("initial authorized tombstone read failed: %q %v", first, err)
			}
			replay, err := invokeContextTool(contextRealIMNode(t, f), f.ctx(), "already-tombstoned", name, args)
			if err != nil || replay != first {
				t.Fatalf("unchanged ACL/tombstone cannot replay committed successful receipt: %v", err)
			}
		})
	}
}

func TestCachedIMReadRejectsRevocationWithoutChangingReceipt(t *testing.T) {
	for _, name := range []string{"im_get_message", "im_list_messages"} {
		for _, change := range []string{"conversation_removed", "workspace_removed", "retracted_later", "message_missing", "sender_changed", "invalid_tombstone"} {
			t.Run(name+"/"+change, func(t *testing.T) {
				f := newContextArchiveFixture(t)
				message := contextAddForeignMessage(f, "")
				stamp := "2026-10-28T00:00:00Z"
				message.RetractedAt = &stamp
				if change == "retracted_later" {
					message.RetractedAt = nil
					message.Body = "private original text"
				}
				if change == "invalid_tombstone" {
					message.Body = "private text must not qualify as a tombstone"
				}
				f.mu.Lock()
				f.messages[message.ID] = message
				f.mu.Unlock()
				var args any = getMessageArgs{ConversationID: message.ConversationID, MessageID: message.ID}
				if name == "im_list_messages" {
					args = listMessagesArgs{ConversationID: message.ConversationID}
				}
				const callID = "revoked-read"
				if first, err := invokeContextTool(contextRealIMNode(t, f), f.ctx(), callID, name, args); err != nil || !strings.Contains(first, `"ok":true`) {
					t.Fatalf("initial read failed: %q %v", first, err)
				}
				var before, after []byte
				if err := f.s.DB.QueryRow(f.ctx(), `SELECT result FROM runtime_tool_calls WHERE run_id=$1 AND call_id=$2`, f.run.ID, callID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				f.mu.Lock()
				switch change {
				case "conversation_removed":
					f.deniedConversations[message.ConversationID] = true
				case "workspace_removed":
					f.deniedWorkspaces[f.conversations[message.ConversationID].WorkspaceID] = true
				case "retracted_later", "invalid_tombstone":
					message.Body = ""
					message.RetractedAt = &stamp
					f.messages[message.ID] = message
				case "message_missing":
					delete(f.messages, message.ID)
				case "sender_changed":
					message.SenderID = f.l.AgentID
					f.messages[message.ID] = message
				}
				f.mu.Unlock()
				if out, err := invokeContextTool(contextRealIMNode(t, f), f.ctx(), callID, name, args); !errors.Is(err, errContextUnavailable) || out != "" {
					t.Fatalf("unsafe replay allowed: %q %v", out, err)
				}
				if err := f.s.DB.QueryRow(f.ctx(), `SELECT result FROM runtime_tool_calls WHERE run_id=$1 AND call_id=$2`, f.run.ID, callID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("replay changed the durable receipt")
				}
			})
		}
	}
}

func TestCachedMixedMessagePageDoesNotExemptLiveContent(t *testing.T) {
	f := newContextArchiveFixture(t)
	tombstone := contextAddForeignMessage(f, "")
	stamp := "2026-10-28T00:00:00Z"
	tombstone.RetractedAt = &stamp
	live := contextAddForeignMessage(f, "private live content")
	live.ConversationID = tombstone.ConversationID
	live.Seq = tombstone.Seq + 1
	f.mu.Lock()
	f.messages[tombstone.ID] = tombstone
	f.messages[live.ID] = live
	f.mu.Unlock()
	args := listMessagesArgs{ConversationID: tombstone.ConversationID}
	first, err := invokeContextTool(contextRealIMNode(t, f), f.ctx(), "mixed-page", "im_list_messages", args)
	if err != nil || !strings.Contains(first, live.Body) {
		t.Fatalf("initial page: %q %v", first, err)
	}
	replayed, err := invokeContextTool(contextRealIMNode(t, f), f.ctx(), "mixed-page", "im_list_messages", args)
	if err != nil || replayed != first {
		t.Fatalf("mixed page did not replay exactly: %v", err)
	}
	f.mu.Lock()
	live.Body = ""
	live.RetractedAt = &stamp
	f.messages[live.ID] = live
	f.mu.Unlock()
	if _, err = invokeContextTool(contextRealIMNode(t, f), f.ctx(), "mixed-page", "im_list_messages", args); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("later-retracted body replayed: %v", err)
	}
}
