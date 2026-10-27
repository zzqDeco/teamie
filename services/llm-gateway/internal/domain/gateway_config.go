package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
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

// ModelRoutingConfig stores administrator intent independently of model availability.
// An unavailable selected default stays selected; resolution never changes it.
type ModelRoutingConfig struct {
	DefaultModelID string
	UpdatedAt      time.Time
}

// WebSearchConfig owns only the search capability's state and credential.
type WebSearchConfig struct {
	CipherKey []byte `json:"-"`
	Enabled   bool
	UpdatedAt time.Time
}

func (c WebSearchConfig) Configured() bool { return len(c.CipherKey) > 0 }

// Config is a read snapshot. Writes use separate catalog, routing and search ports.
type Config struct {
	Models    []ModelProfile
	Routing   ModelRoutingConfig
	WebSearch WebSearchConfig
}

// ResolvedModel is a per-call model snapshot, never a configuration write command.
type ResolvedModel ModelProfile

func (m ResolvedModel) Configured() bool         { return ModelProfile(m).Configured() }
func (m ResolvedModel) ContextWindow() int64     { return ModelProfile(m).ContextWindow() }
func (m ResolvedModel) CountingConfigID() string { return ModelProfile(m).CountingConfigID() }
func (m ResolvedModel) Status() string {
	if !m.Configured() {
		return "unconfigured"
	}
	if !m.Enabled {
		return "disabled"
	}
	return "ready"
}

func (c Config) EffectiveDefaultModelID() string { return c.ResolveModel("").ID }

// ResolveModel falls back without mutating routing, catalog entries or search settings.
func (c Config) ResolveModel(requestedModelID string) ResolvedModel {
	available := func(id string) (ResolvedModel, bool) {
		for _, m := range c.Models {
			if m.ID == id && m.Configured() && m.Enabled {
				return ResolvedModel(m), true
			}
		}
		return ResolvedModel{}, false
	}
	if m, ok := available(strings.TrimSpace(requestedModelID)); ok {
		return m
	}
	if m, ok := available(c.Routing.DefaultModelID); ok {
		return m
	}
	for _, m := range c.Models {
		if m.Configured() && m.Enabled {
			return ResolvedModel(m)
		}
	}
	// Preserve disabled/unconfigured status when no usable model exists.
	for _, m := range c.Models {
		if m.ID == c.Routing.DefaultModelID {
			return ResolvedModel(m)
		}
	}
	if len(c.Models) > 0 {
		return ResolvedModel(c.Models[0])
	}
	return ResolvedModel{}
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
