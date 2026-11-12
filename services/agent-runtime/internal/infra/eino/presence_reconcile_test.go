package eino

import (
	"testing"
	"time"
)

func TestReconcileUnreadClearsActiveConversationAndUnreadOnGroupRemoval(t *testing.T) {
	p := newConversationPresenceState(time.Minute, nil)
	wsID := "00000000-0000-4000-8000-000000000001"
	agentID := "00000000-0000-4000-8000-000000000009"
	channel := Conversation{ID: "11111111-1111-4111-8111-111111111111", WorkspaceID: wsID, Type: "channel", Name: "general"}
	group := Conversation{ID: "22222222-2222-4222-8222-222222222222", WorkspaceID: wsID, Type: "group", Name: "sprint"}

	p.Enter(wsID, group.ID, "group")
	p.RecordUnread(wsID, group, &Message{ID: "m-1", SenderID: "human-1", SenderKind: "human", Seq: 3, Body: "hello"}, agentID, time.Now().UTC(), false)
	p.RecordUnread(wsID, channel, &Message{ID: "m-2", SenderID: "human-1", SenderKind: "human", Seq: 5, Body: "ping"}, agentID, time.Now().UTC(), true)

	if got := p.ActiveConversationID(); got != group.ID {
		t.Fatalf("expected activeConversationID=%s, got %s", group.ID, got)
	}

	// Simulate group dissolution or self-leave: bootstrap now contains only channel.
	if changed := p.ReconcileUnread([]Conversation{channel}); !changed {
		t.Fatal("expected ReconcileUnread to report changed=true when active/unread group disappears")
	}
	if got := p.ActiveConversationID(); got != "" {
		t.Fatalf("expected activeConversationID to be cleared after group removal, got %q", got)
	}
	enrichedGroup := p.EnrichConversation(group)
	if enrichedGroup.Active || enrichedGroup.UnreadCount != 0 {
		t.Fatalf("expected removed group to have no active/unread state, got %+v", enrichedGroup)
	}
	enrichedChannel := p.EnrichConversation(channel)
	if enrichedChannel.UnreadCount != 1 || enrichedChannel.MentionCount != 1 {
		t.Fatalf("expected surviving channel unread state to remain intact, got %+v", enrichedChannel)
	}

	// Also verify when only activeConversationID (with zero unread) disappears.
	p.Enter(wsID, group.ID, "group")
	if changed := p.ReconcileUnread([]Conversation{channel}); !changed {
		t.Fatal("expected ReconcileUnread to report changed=true when active-only group disappears")
	}
	if got := p.ActiveConversationID(); got != "" {
		t.Fatalf("expected activeConversationID to be cleared, got %q", got)
	}
}
