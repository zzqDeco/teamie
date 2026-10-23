package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	TransportVersion         = "teamie-agentic-chat-v1"
	DefaultContextWindow     = int64(16384)
	MinContextWindow         = int64(4096)
	MaxContextWindow         = int64(2000000)
	MaxRequestBytes          = 16 << 20
	MaxResponseBytes         = 8 << 20
	MaxOutputTokens          = 131072
	LegacyMaxInputMessages   = 128
	MaxInputMessages         = 65536
	MaxProviderStreamBytes   = 128 << 20
)

// CountingConfigID returns an opaque, secret-free SHA-256 digest identifying the
// active gateway model/tokenizer configuration (transport version, canonical
// baseURL, and model name). It never exposes provider URLs or credentials.
func CountingConfigID(baseURL, modelName string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	model := strings.TrimSpace(modelName)
	if base == "" || model == "" {
		return ""
	}
	payload, _ := json.Marshal(struct {
		Version string `json:"version"`
		BaseURL string `json:"baseUrl"`
		Model   string `json:"model"`
	}{
		Version: TransportVersion,
		BaseURL: base,
		Model:   model,
	})
	sum := sha256.Sum256(payload)
	return "modelcfg:v1:" + hex.EncodeToString(sum[:16])
}

// Config is the domain entity for platform-administered model and web-search settings.
type Config struct {
	BaseURL             string    `json:"baseUrl"`
	Model               string    `json:"model"`
	CipherKey           []byte    `json:"-"`
	Enabled             bool      `json:"enabled"`
	UpdatedAt           time.Time `json:"updatedAt"`
	ContextWindowTokens int64     `json:"contextWindowTokens"`
	WebSearchCipherKey  []byte    `json:"-"`
	WebSearchEnabled    bool      `json:"webSearchEnabled"`
	WebSearchEnabledSet bool      `json:"-"`
	WebSearchAPIKey     *string   `json:"-"`
	ClearWebSearchKey   bool      `json:"-"`
}

func (c Config) ContextWindow() int64 {
	if c.ContextWindowTokens == 0 {
		return DefaultContextWindow
	}
	return c.ContextWindowTokens
}

func (c Config) Configured() bool {
	return c.BaseURL != "" && c.Model != "" && len(c.CipherKey) > 0
}

func (c Config) WebSearchConfigured() bool {
	return len(c.WebSearchCipherKey) > 0
}

func (c Config) CountingConfigID() string {
	if !c.Configured() {
		return ""
	}
	return CountingConfigID(c.BaseURL, c.Model)
}

func (c Config) Status() string {
	if !c.Configured() {
		return "unconfigured"
	}
	if c.Enabled {
		return "ready"
	}
	return "disabled"
}

func (c Config) View() map[string]any {
	return map[string]any{
		"config": map[string]any{
			"baseUrl":             c.BaseURL,
			"model":               c.Model,
			"contextWindowTokens": c.ContextWindow(),
			"hasApiKey":           len(c.CipherKey) > 0,
			"enabled":             c.Enabled,
			"configured":          c.Configured(),
			"webSearch": map[string]any{
				"provider":   "tavily",
				"enabled":    c.WebSearchEnabled,
				"hasApiKey":  len(c.WebSearchCipherKey) > 0,
				"configured": c.WebSearchConfigured(),
			},
			"updatedAt": c.UpdatedAt,
		},
		"status": c.Status(),
	}
}

// MergeSavedConfig applies key preservation, explicit key clearing, encryption,
// and context-window invariants when updating gateway configuration.
func MergeSavedConfig(old, c Config, key *string, clear bool, crypto Cipher) (Config, error) {
	var err error
	c.CipherKey = old.CipherKey
	c.WebSearchCipherKey = old.WebSearchCipherKey
	if !c.WebSearchEnabledSet && !c.WebSearchEnabled {
		c.WebSearchEnabled = old.WebSearchEnabled
	}
	if c.ContextWindowTokens == 0 {
		c.ContextWindowTokens = old.ContextWindow()
	}
	if c.ContextWindowTokens < MinContextWindow || c.ContextWindowTokens > MaxContextWindow {
		return Config{}, errors.New("invalid context window")
	}
	if clear {
		c.CipherKey = nil
	}
	if key != nil {
		c.CipherKey, err = crypto.Encrypt(*key)
		if err != nil {
			return Config{}, err
		}
	}
	if c.ClearWebSearchKey {
		c.WebSearchCipherKey = nil
	}
	if c.WebSearchAPIKey != nil {
		c.WebSearchCipherKey, err = crypto.Encrypt(*c.WebSearchAPIKey)
		if err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// Usage represents a single model call audit record.
type Usage struct {
	OwnerID, AgentID, SessionID, RunID, Status string
	PromptTokens, CompletionTokens             int64
	LatencyMS                                  int64
}

// ValidID checks whether an attribution ID is non-empty, bounded, and free of control characters.
func ValidID(v string) bool {
	return v != "" && len(v) <= 160 && !strings.ContainsAny(v, "\r\n\x00")
}

// EqualSecret compares two secret strings of length >= 24 in constant time.
func EqualSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return len(a) >= 24 && len(b) >= 24 && subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
