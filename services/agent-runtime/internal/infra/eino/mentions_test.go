package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/compose"
	"teamie/pkg/imtext"
)

func TestStructuredMentionsNativeSessionReceivesAllInputsAndSendsIdentity(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	flags := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flags, []byte(`{"revision":"mentions-test","flags":{"agents.runtime.v3":true,"agents.tasks.execute":true,"agents.context.v1":true,"agents.mentions.v1":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.Config.FlagsFile = flags
	f.add("@Agent 处理这个请求", "")
	var source *Message
	for _, m := range f.messages {
		source = m
	}
	source.Mentions = []imtext.Mention{{UserID: f.agent, Start: 0, End: 6}}
	gw := &toolPathGateway{fixtureGateway: base}
	gw.steps = []func(toolPathRequest) (chatMessage, error){
		func(req toolPathRequest) (chatMessage, error) {
			var input Input
			for _, m := range req.Messages {
				if strings.HasPrefix(m.Content, "TEAMIE_NEW_MESSAGE ") {
					_ = json.Unmarshal([]byte(strings.TrimPrefix(m.Content, "TEAMIE_NEW_MESSAGE ")), &input)
				}
			}
			if !reflect.DeepEqual(input.Mentions, source.Mentions) || input.SenderID != source.SenderID || input.MessageID != source.ID {
				return chatMessage{}, fmt.Errorf("structured recipient or true source was lost: %+v", input)
			}
			for _, item := range req.Tools {
				if item.Function.Name == "im_send_message" {
					raw, _ := json.Marshal(item)
					if !bytes.Contains(raw, []byte(`"mentions"`)) || bytes.Contains(raw, []byte(`"attachmentIds"`)) {
						return chatMessage{}, fmt.Errorf("wrong immutable mention-only manifest")
					}
				}
			}
			return toolPathCall("im_send_message", "mention-reply", mentionedSendArgs{ConversationID: f.conversation, Body: "@伙伴 已收到", ReplyToID: source.ID, Mentions: []imtext.Mention{{UserID: source.SenderID, Start: 0, End: 3}}})
		},
		func(req toolPathRequest) (chatMessage, error) {
			observation, err := toolPathLast(req, "mention-reply")
			var message Message
			if err != nil || !observation.OK || json.Unmarshal(observation.Data, &message) != nil || len(message.Mentions) != 1 || message.Mentions[0].UserID != source.SenderID || message.ReplyToID != source.ID {
				return chatMessage{}, fmt.Errorf("native mention reply was not committed")
			}
			return chatMessage{Role: "assistant", Content: "PRIVATE_MENTION_FINAL"}, nil
		},
		func(req toolPathRequest) (chatMessage, error) {
			if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "unmentioned group chatter") {
				return chatMessage{}, fmt.Errorf("unmentioned message was filtered out of the TurnLoop")
			}
			return chatMessage{Role: "assistant", Content: "PRIVATE_SILENCE"}, nil
		},
	}
	e.Gateway = gw
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) == 1
	})
	f.add("unmentioned group chatter", "")
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) == 2
	})
	cancel()
	<-done
	gw.pathMu.Lock()
	defer gw.pathMu.Unlock()
	if gw.failure != nil || len(gw.requests) != 3 || count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) != 2 || count(t, s, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
		t.Fatal(gw.failure, len(gw.requests))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 1 {
		t.Fatal("chatter or private finals became IM messages", f.sent)
	}
}

func TestStructuredMentionManifestDoesNotChangeExistingRunSchemas(t *testing.T) {
	old, _ := newContextBudgetWithFiles(16384, true)
	if bytes.Contains(old, []byte("structuredMentions")) || mentionsForRun(&Run{ContextProfile: contextProfileV1, ContextBudget: old}) {
		t.Fatal("existing file Run gained mentions")
	}
	current, _ := newContextBudgetWithFiles(16384, true, true)
	if !mentionsForRun(&Run{ContextProfile: contextProfileV1, ContextBudget: current}) {
		t.Fatal("new Run did not freeze mention admission")
	}
	for _, files := range []bool{false, true} {
		tools := &Tools{}
		oldItems, _ := tools.buildIM(files)
		newItems, err := tools.BuildIMWithMentions(files)
		if err != nil {
			t.Fatal(err)
		}
		oldInfo, _ := oldItems[3].Info(context.Background())
		oldSchema, _ := oldInfo.ParamsOneOf.ToJSONSchema()
		oldRaw, _ := json.Marshal(oldSchema)
		newInfo, _ := newItems[3].Info(context.Background())
		newSchema, _ := newInfo.ParamsOneOf.ToJSONSchema()
		newRaw, _ := json.Marshal(newSchema)
		if bytes.Contains(oldRaw, []byte(`"mentions"`)) || !bytes.Contains(newRaw, []byte(`"mentions"`)) || bytes.Contains(newRaw, []byte(`"attachmentIds"`)) != files {
			t.Fatal(string(oldRaw), string(newRaw))
		}
	}
}

func TestStructuredMentionReceiptArchivesReorderedToolArguments(t *testing.T) {
	f, state := newDeliveryFixture(t, true)
	tools := &Tools{Store: f.s, Lease: f.l, IM: f.a.IM, Sandbox: state.sandbox, SandboxToken: "delivery-sandbox-token"}
	items, err := tools.BuildIMWithMentions(true)
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: items, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	// Map JSON orders fields differently from InferTool's parsed struct.
	args := map[string]any{"path": "/workspace/results/report.bin", "filename": "report.bin", "conversationId": f.input.ConversationID}
	result, err := invokeContextTool(node, f.ctx(), "reordered-upload", "im_upload_file", args)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", contextRealIMDetail("im_upload_file", "reordered-upload", args, result), result)
	if err != nil {
		t.Fatal("typed upload arguments lost provenance under ordinary model field ordering", err)
	}
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); err != nil {
		t.Fatal(err)
	}
}
