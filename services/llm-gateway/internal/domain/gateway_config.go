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
	TransportVersion       = "teamie-agentic-chat-v1"
	DefaultContextWindow   = int64(16384)
	MinContextWindow       = int64(4096)
	MaxContextWindow       = int64(2000000)
	MaxRequestBytes        = 16 << 20
	MaxResponseBytes       = 8 << 20
	MaxOutputTokens        = 131072
	LegacyMaxInputMessages = 128
	MaxInputMessages       = 65536
	MaxProviderStreamBytes = 128 << 20
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

// ModelProfile represents a single administrator-configured model option in the gateway.
type ModelProfile struct {
	ID                  string    `json:"id"`
	DisplayName         string    `json:"displayName"`
	Description         string    `json:"description"`
	BaseURL             string    `json:"baseUrl"`
	Model               string    `json:"model"`
	CipherKey           []byte    `json:"-"`
	ContextWindowTokens int64     `json:"contextWindowTokens"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

func (m ModelProfile) ContextWindow() int64 {
	if m.ContextWindowTokens == 0 {
		return DefaultContextWindow
	}
	return m.ContextWindowTokens
}

func (m ModelProfile) Configured() bool {
	return m.BaseURL != "" && m.Model != "" && len(m.CipherKey) > 0
}

func (m ModelProfile) CountingConfigID() string {
	if !m.Configured() {
		return ""
	}
	return CountingConfigID(m.BaseURL, m.Model)
}

// ValidModelID checks whether a model profile ID is a safe URL-path identifier ([a-zA-Z0-9._-], 1..64 chars).
func ValidModelID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		isAlphaNum := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if !isAlphaNum && ch != '-' && ch != '_' && ch != '.' {
			return false
		}
	}
	return id != "." && id != ".."
}

// SuggestModelID derives a clean URL-safe model ID slug from displayName or modelName, avoiding existing IDs.
func SuggestModelID(displayName, modelName string, existing []ModelProfile) string {
	slugify := func(s string) string {
		var b strings.Builder
		lastDash := false
		for _, r := range strings.ToLower(strings.TrimSpace(s)) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
				lastDash = false
			} else if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
			if b.Len() >= 48 {
				break
			}
		}
		return strings.Trim(b.String(), "-")
	}
	base := slugify(modelName)
	if base == "" {
		base = slugify(displayName)
	}
	if base == "" {
		base = "model"
	}
	taken := make(map[string]bool, len(existing))
	for _, m := range existing {
		taken[m.ID] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := base + "-" + strconvItoa(i)
		if !taken[candidate] {
			return candidate
		}
	}
	return base
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Config is the domain entity for platform-administered model and web-search settings.
type Config struct {
	BaseURL             string                               `json:"baseUrl"`
	Model               string                               `json:"model"`
	CipherKey           []byte                               `json:"-"`
	Enabled             bool                                 `json:"enabled"`
	UpdatedAt           time.Time                            `json:"updatedAt"`
	ContextWindowTokens int64                                `json:"contextWindowTokens"`
	WebSearchCipherKey  []byte                               `json:"-"`
	WebSearchEnabled    bool                                 `json:"webSearchEnabled"`
	WebSearchEnabledSet bool                                 `json:"-"`
	WebSearchAPIKey     *string                              `json:"-"`
	ClearWebSearchKey   bool                                 `json:"-"`
	DefaultModelID      string                               `json:"defaultModelId,omitempty"`
	Models              []ModelProfile                       `json:"models,omitempty"`
	ModelsMutation      func(old Config, c Cipher) (Config, error) `json:"-"`
}

func (c Config) EffectiveModels() []ModelProfile {
	if len(c.Models) > 0 {
		out := make([]ModelProfile, len(c.Models))
		copy(out, c.Models)
		return out
	}
	if c.BaseURL == "" && c.Model == "" && len(c.CipherKey) == 0 {
		return nil
	}
	defID := strings.TrimSpace(c.DefaultModelID)
	if defID == "" {
		defID = "default"
	}
	return []ModelProfile{{
		ID:                  defID,
		DisplayName:         c.Model,
		Description:         "",
		BaseURL:             c.BaseURL,
		Model:               c.Model,
		CipherKey:           c.CipherKey,
		ContextWindowTokens: c.ContextWindow(),
		Enabled:             c.Enabled,
		CreatedAt:           c.UpdatedAt,
		UpdatedAt:           c.UpdatedAt,
	}}
}

func (c Config) EffectiveDefaultModelID() string {
	models := c.EffectiveModels()
	if len(models) == 0 {
		return ""
	}
	defID := strings.TrimSpace(c.DefaultModelID)
	if defID != "" {
		for _, m := range models {
			if m.ID == defID && m.Configured() && m.Enabled {
				return m.ID
			}
		}
	}
	for _, m := range models {
		if m.Configured() && m.Enabled {
			return m.ID
		}
	}
	if defID != "" {
		for _, m := range models {
			if m.ID == defID {
				return m.ID
			}
		}
	}
	return models[0].ID
}

func (c Config) withModelProfile(m ModelProfile) Config {
	out := c
	out.BaseURL = m.BaseURL
	out.Model = m.Model
	out.CipherKey = m.CipherKey
	out.ContextWindowTokens = m.ContextWindow()
	out.Enabled = m.Enabled
	out.DefaultModelID = m.ID
	if !m.UpdatedAt.IsZero() {
		out.UpdatedAt = m.UpdatedAt
	}
	return out
}

// ResolveModel resolves requestedModelID to its active ModelProfile configuration.
// When requestedModelID is empty, deleted, unconfigured, or disabled, it smoothly
// falls back to the platform's current default model.
func (c Config) ResolveModel(requestedModelID string) Config {
	models := c.EffectiveModels()
	if len(models) == 0 {
		return c
	}
	reqID := strings.TrimSpace(requestedModelID)
	if reqID != "" {
		for _, m := range models {
			if m.ID == reqID && m.Configured() && m.Enabled {
				return c.withModelProfile(m)
			}
		}
	}
	defID := strings.TrimSpace(c.DefaultModelID)
	if defID != "" {
		for _, m := range models {
			if m.ID == defID && m.Configured() && m.Enabled {
				return c.withModelProfile(m)
			}
		}
	}
	for _, m := range models {
		if m.Configured() && m.Enabled {
			return c.withModelProfile(m)
		}
	}
	if defID != "" {
		for _, m := range models {
			if m.ID == defID {
				return c.withModelProfile(m)
			}
		}
	}
	return c.withModelProfile(models[0])
}

// SyncPrimaryFromModels updates the top-level primary Config fields from c.Models and c.DefaultModelID.
func (c *Config) SyncPrimaryFromModels() {
	if len(c.Models) == 0 {
		c.DefaultModelID = "default"
		c.BaseURL = ""
		c.Model = ""
		c.CipherKey = nil
		c.Enabled = false
		if c.ContextWindowTokens == 0 {
			c.ContextWindowTokens = DefaultContextWindow
		}
		return
	}
	effID := c.EffectiveDefaultModelID()
	if strings.TrimSpace(c.DefaultModelID) == "" {
		c.DefaultModelID = effID
	} else {
		found := false
		for _, m := range c.Models {
			if m.ID == c.DefaultModelID {
				found = true
				break
			}
		}
		if !found {
			c.DefaultModelID = effID
		}
	}
	var primary ModelProfile
	found := false
	for _, m := range c.Models {
		if m.ID == effID {
			primary = m
			found = true
			break
		}
	}
	if !found {
		primary = c.Models[0]
	}
	c.BaseURL = primary.BaseURL
	c.Model = primary.Model
	c.CipherKey = primary.CipherKey
	c.ContextWindowTokens = primary.ContextWindow()
	c.Enabled = primary.Enabled
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
	if c.ModelsMutation != nil {
		return c.ModelsMutation(old, crypto)
	}
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
	defID := strings.TrimSpace(old.DefaultModelID)
	if defID == "" {
		defID = "default"
	}
	c.DefaultModelID = defID
	c.Models = make([]ModelProfile, len(old.Models))
	copy(c.Models, old.Models)
	if clear && len(c.CipherKey) == 0 {
		filtered := c.Models[:0]
		for _, m := range c.Models {
			if m.ID != defID {
				filtered = append(filtered, m)
			}
		}
		c.Models = filtered
		if len(c.Models) > 0 {
			c.SyncPrimaryFromModels()
		}
	} else if c.BaseURL != "" || c.Model != "" || len(c.CipherKey) > 0 {
		now := time.Now().UTC()
		updated := false
		for i := range c.Models {
			if c.Models[i].ID == defID {
				if c.Models[i].DisplayName == "" || c.Models[i].DisplayName == old.Model {
					c.Models[i].DisplayName = c.Model
				}
				c.Models[i].BaseURL = c.BaseURL
				c.Models[i].Model = c.Model
				c.Models[i].CipherKey = c.CipherKey
				c.Models[i].ContextWindowTokens = c.ContextWindow()
				c.Models[i].Enabled = c.Enabled
				c.Models[i].UpdatedAt = now
				updated = true
				break
			}
		}
		if !updated {
			c.Models = append(c.Models, ModelProfile{
				ID:                  defID,
				DisplayName:         c.Model,
				BaseURL:             c.BaseURL,
				Model:               c.Model,
				CipherKey:           c.CipherKey,
				ContextWindowTokens: c.ContextWindow(),
				Enabled:             c.Enabled,
				CreatedAt:           now,
				UpdatedAt:           now,
			})
		}
	}
	return c, nil
}

// Usage represents a single model call audit record.
type Usage struct {
	OwnerID, AgentID, SessionID, RunID, ModelID, Status string
	PromptTokens, CompletionTokens                      int64
	LatencyMS                                           int64
}

// StreamEvent is the private NDJSON transport event emitted during a streaming model call.
// A response has exactly one terminal done/error event.
type StreamEvent struct {
	Type          string `json:"type"`
	ResponseJSON  string `json:"responseJson,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
	StatusCode    int    `json:"statusCode,omitempty"`
	ModelConfigID string `json:"modelConfigId,omitempty"`
}

// ChatCallResult represents the result of a unary model call.
type ChatCallResult struct {
	OK            bool
	ResponseJSON  string
	ErrorCode     string
	ErrorMessage  string
	StatusCode    int
	ModelConfigID string
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
