package domain

import "context"

// Cipher encrypts and decrypts stored provider credentials.
type Cipher interface {
	Encrypt(key string) ([]byte, error)
	Decrypt(data []byte) (string, error)
}

// ConfigStore persists and retrieves model gateway configuration and usage records.
type ConfigStore interface {
	Load(ctx context.Context) (Config, error)
	Save(ctx context.Context, c Config, key *string, clear bool, crypto Cipher) (Config, error)
	Record(ctx context.Context, u Usage) error
	Ready(ctx context.Context) error
}

// UpstreamModel executes unary or streaming model inference against the configured provider.
type UpstreamModel interface {
	Chat(ctx context.Context, c Config, key, raw string, messageLimits ...int) (body string, prompt, completion int64, err error)
	Stream(ctx context.Context, c Config, key, raw string, emit func(string) error, messageLimits ...int) (prompt, completion int64, err error)
}

// WebSearchProvider executes Tavily search and extract calls.
type WebSearchProvider interface {
	Search(ctx context.Context, baseURL, apiKey string, payload map[string]any, maxResults int) (results []WebSearchItem, respBytes int, err error)
	Extract(ctx context.Context, baseURL, apiKey string, payload map[string]any) (results []WebExtractItem, failed []WebExtractFailure, respBytes int, err error)
}
