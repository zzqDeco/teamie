// Package eino encapsulates CloudWeGo Eino (v0.10.0-alpha.35) ADK, schema,
// and tool execution primitives behind clean helper interfaces.
package eino

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"teamie/agent-runtime/internal/engine/domain"
)

// MarshalHumanReadable serializes an Eino schema/ADK value using HumanReadableSerializer.
func MarshalHumanReadable(v any) ([]byte, error) {
	return (&schema.HumanReadableSerializer{}).Marshal(v)
}

// UnmarshalHumanReadable deserializes an Eino schema/ADK value using HumanReadableSerializer.
func UnmarshalHumanReadable(data []byte, v any) error {
	return (&schema.HumanReadableSerializer{}).Unmarshal(data, v)
}

// IsWillRetryError reports whether err is a transient Eino ADK WillRetryError observation.
func IsWillRetryError(err error) bool {
	var retry *adk.WillRetryError
	return errors.As(err, &retry)
}

// IsCancelError reports whether err is an Eino ADK CancelError.
func IsCancelError(err error) bool {
	var c *adk.CancelError
	return errors.As(err, &c)
}

// IsNonRetryableEventError reports whether err is a permanent Eino session event identity error.
func IsNonRetryableEventError(err error) bool {
	return errors.Is(err, adk.ErrInvalidEventID) || errors.Is(err, adk.ErrDuplicateEventID)
}

// IsRetryableEventPersistenceError reports whether err is a retryable Eino session event persistence error.
func IsRetryableEventPersistenceError(err error) bool {
	return errors.Is(err, adk.ErrSessionEventPersistence)
}

// TargetsFromCancelError extracts the interrupt target IDs from an Eino ADK CancelError.
func TargetsFromCancelError(err error) []string {
	var c *adk.CancelError
	if !errors.As(err, &c) {
		return nil
	}
	out := []string{}
	for _, v := range c.InterruptContexts {
		if v != nil && v.ID != "" {
			out = append(out, v.ID)
		}
	}
	return out
}

// FilterToolsForProfile filters the available Eino tools according to the Run's frozen tool profile.
func FilterToolsForProfile(ctx context.Context, available []tool.BaseTool, toolProfile string) ([]tool.BaseTool, error) {
	if toolProfile != domain.V034ToolProfile {
		return available, nil
	}
	previous := make([]tool.BaseTool, 0, len(available))
	for _, item := range available {
		info, err := item.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info.Name != "im_get_attachment" {
			previous = append(previous, item)
		}
	}
	return previous, nil
}

// FormatInputEnvelope returns the canonical user message text for a durable input item.
func FormatInputEnvelope(kind string, rawJSON []byte) string {
	prefix := "TEAMIE_NEW_MESSAGE "
	if kind == "task.notification" {
		prefix = "TEAMIE_TASK_NOTIFICATION "
	} else if kind == "schedule.wakeup" {
		prefix = "TEAMIE_SCHEDULE_WAKEUP "
	}
	return prefix + string(rawJSON)
}

// BuildInputMessages converts a slice of serializable input items into Eino UserAgenticMessages.
func BuildInputMessages[T any](inputs []T, kindOf func(T) string) []*schema.AgenticMessage {
	out := make([]*schema.AgenticMessage, 0, len(inputs))
	for _, in := range inputs {
		b, _ := json.Marshal(in)
		out = append(out, schema.UserAgenticMessage(FormatInputEnvelope(kindOf(in), b)))
	}
	return out
}
