package domain

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// StreamGuardPolicy groups streaming credential-suppression and request normalization rules.
type StreamGuardPolicy struct{}

// MaxTools bounds declarations while allowing Runtime's complete tool set.
// The transport byte and context token budgets remain independent limits.
const MaxTools = 128

// CredentialResponseError returns the canonical error when a provider leaks its credential.
func CredentialResponseError() error {
	return Err(502, "invalid_upstream_response", "Provider returned a credential in its response")
}

// InputMessageLimit resolves the effective input message ceiling.
func InputMessageLimit(limits ...int) int {
	if len(limits) == 0 || limits[0] == 0 {
		return LegacyMaxInputMessages
	}
	return min(limits[0], MaxInputMessages)
}

// NormalizeModeWithOutputLimit normalizes and validates an outbound OpenAI chat completions payload.
func NormalizeModeWithOutputLimit(raw, model string, streaming bool, outputLimit int, messageLimits ...int) ([]byte, error) {
	if len(raw) > MaxRequestBytes {
		return nil, Err(413, "request_too_large", "Model request is too large")
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil || m == nil {
		return nil, Err(400, "invalid_request", "Invalid chat completion JSON")
	}
	allowed := map[string]bool{
		"messages": true, "model": true, "stream": true, "stream_options": true,
		"tools": true, "tool_choice": true, "parallel_tool_calls": true,
		"temperature": true, "top_p": true, "max_tokens": true,
		"max_completion_tokens": true, "stop": true, "response_format": true,
		"seed": true, "frequency_penalty": true, "presence_penalty": true,
		"n": true, "enable_thinking": true,
	}
	for k := range m {
		if !allowed[k] {
			return nil, Err(400, "invalid_request", "Unsupported chat completion option")
		}
	}
	if raw, ok := m["enable_thinking"]; ok {
		if value := strings.TrimSpace(string(raw)); value != "true" && value != "false" {
			return nil, Err(400, "invalid_request", "Invalid thinking option")
		}
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(m["messages"], &messages) != nil || len(messages) == 0 || len(messages) > InputMessageLimit(messageLimits...) {
		return nil, Err(400, "invalid_request", "Invalid chat message count")
	}
	for _, msg := range messages {
		var role string
		_ = json.Unmarshal(msg["role"], &role)
		switch role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return nil, Err(400, "invalid_request", "Unsupported message role")
		}
	}
	var stream bool
	if b, ok := m["stream"]; ok && (json.Unmarshal(b, &stream) != nil || stream && !streaming) {
		return nil, Err(400, "stream_unsupported", "Use non-streaming chat completions")
	}
	var tools []json.RawMessage
	if b, ok := m["tools"]; ok && (json.Unmarshal(b, &tools) != nil || len(tools) > MaxTools) {
		return nil, Err(400, "invalid_request", "At most 128 tools are supported")
	}
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		if b, ok := m[k]; ok {
			var n int
			if json.Unmarshal(b, &n) != nil || n < 1 {
				return nil, Err(400, "invalid_request", "Invalid completion token limit")
			}
			if n > outputLimit {
				m[k] = json.RawMessage(strconv.Itoa(outputLimit))
			}
		}
	}
	if _, ok := m["max_tokens"]; !ok {
		if _, ok = m["max_completion_tokens"]; !ok {
			m["max_completion_tokens"] = json.RawMessage("2048")
		}
	}
	m["model"], _ = json.Marshal(model)
	m["n"] = json.RawMessage("1")
	m["stream"] = json.RawMessage("false")
	delete(m, "stream_options")
	if streaming {
		m["stream"] = json.RawMessage("true")
		m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	return json.Marshal(m)
}

type credentialMatch struct {
	matched int
	owners  []uint64
}

type guardedChunk struct {
	id  uint64
	raw string
}

type credentialField struct {
	path    string
	decoded bool
}

// CredentialStreamGuard incrementally inspects SSE chunks and JSON tool-call
// argument escapes to block credential leakage across split chunks.
type CredentialStreamGuard struct {
	key       string
	failure   []int
	sequence  uint64
	fields    map[credentialField]*credentialMatch
	arguments map[string]*argumentEscapes
	pending   []guardedChunk
}

// NewCredentialStreamGuard creates a KMP-backed incremental stream credential guard.
func NewCredentialStreamGuard(key string) *CredentialStreamGuard {
	g := &CredentialStreamGuard{
		key:       key,
		fields:    map[credentialField]*credentialMatch{},
		arguments: map[string]*argumentEscapes{},
	}
	g.failure = make([]int, len(key))
	for i, j := 1, 0; i < len(key); i++ {
		for j > 0 && key[i] != key[j] {
			j = g.failure[j-1]
		}
		if key[i] == key[j] {
			j++
		}
		g.failure[i] = j
	}
	return g
}

func (g *CredentialStreamGuard) feed(path, value string, owner uint64) error {
	return g.feedField(credentialField{path: path}, value, owner)
}

func (g *CredentialStreamGuard) feedDecoded(path, value string, owner uint64) error {
	return g.feedField(credentialField{path: path, decoded: true}, value, owner)
}

func (g *CredentialStreamGuard) feedField(field credentialField, value string, owner uint64) error {
	if value == "" || g.key == "" {
		return nil
	}
	m := g.fields[field]
	if m == nil {
		m = &credentialMatch{}
		g.fields[field] = m
	}
	for i := 0; i < len(value); i++ {
		for m.matched > 0 && value[i] != g.key[m.matched] {
			m.matched = g.failure[m.matched-1]
			m.owners = m.owners[len(m.owners)-m.matched:]
		}
		if value[i] == g.key[m.matched] {
			m.matched++
			m.owners = append(m.owners, owner)
			if m.matched == len(g.key) {
				return CredentialResponseError()
			}
		}
	}
	return nil
}

type argumentEscapes struct {
	escape    string
	owner     uint64
	high      rune
	highOwner uint64
}

func (a *argumentEscapes) feed(g *CredentialStreamGuard, path, value string, owner uint64) error {
	emit := func(value string, at uint64) error { return g.feedDecoded(path, value, at) }
	emitHigh := func() error {
		if a.high == 0 {
			return nil
		}
		at := a.highOwner
		a.high = 0
		return emit(string(utf8.RuneError), at)
	}
	decoded := func(r rune, at uint64) error {
		if a.high != 0 {
			high, highOwner := a.high, a.highOwner
			a.high = 0
			if r >= 0xdc00 && r <= 0xdfff {
				return emit(string(utf16.DecodeRune(high, r)), highOwner)
			}
			if e := emit(string(utf8.RuneError), highOwner); e != nil {
				return e
			}
		}
		if r >= 0xd800 && r <= 0xdbff {
			a.high, a.highOwner = r, at
			return nil
		}
		if r >= 0xdc00 && r <= 0xdfff {
			r = utf8.RuneError
		}
		return emit(string(r), at)
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		if a.escape == "" {
			if b == '\\' {
				a.escape = "\\"
				a.owner = owner
				continue
			}
			if a.high != 0 {
				if e := emitHigh(); e != nil {
					return e
				}
			}
			if e := emit(string([]byte{b}), owner); e != nil {
				return e
			}
			continue
		}
		a.escape += string(b)
		if a.escape == "\\u" {
			continue
		}
		if strings.HasPrefix(a.escape, "\\u") && len(a.escape) < 6 {
			continue
		}
		at, escaped := a.owner, a.escape
		a.escape = ""
		if strings.HasPrefix(escaped, "\\u") {
			unit, e := strconv.ParseUint(escaped[2:], 16, 16)
			if e != nil {
				return Err(502, "invalid_upstream_response", "Provider sent invalid tool argument escapes")
			}
			if e = decoded(rune(unit), at); e != nil {
				return e
			}
			continue
		}
		var result string
		if json.Unmarshal([]byte(`"`+escaped+`"`), &result) != nil {
			return Err(502, "invalid_upstream_response", "Provider sent invalid tool argument escapes")
		}
		if a.high != 0 {
			if e := emitHigh(); e != nil {
				return e
			}
		}
		if e := emit(result, at); e != nil {
			return e
		}
	}
	return nil
}

func (a *argumentEscapes) earliest() uint64 {
	if a.high != 0 {
		return a.highOwner
	}
	if a.escape != "" {
		return a.owner
	}
	return 0
}

// DecodedCredential recursively inspects a decoded JSON value for key.
func DecodedCredential(value any, key string) bool {
	if key == "" {
		return false
	}
	switch v := value.(type) {
	case string:
		return strings.Contains(v, key)
	case map[string]any:
		for name, child := range v {
			if strings.Contains(name, key) || DecodedCredential(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if DecodedCredential(child, key) {
				return true
			}
		}
	}
	return false
}

// GuardSingleResponse validates a complete unary JSON response against credential leakage.
func GuardSingleResponse(raw, key string) error {
	if key == "" {
		return nil
	}
	var document any
	if json.Unmarshal([]byte(raw), &document) != nil {
		return Err(502, "invalid_upstream_response", "Provider sent invalid JSON")
	}
	if DecodedCredential(document, key) {
		return CredentialResponseError()
	}
	g := NewCredentialStreamGuard(key)
	if e := g.WalkDelta("response", document, 1); e != nil {
		return e
	}
	return g.Finish(func(string) error { return nil })
}

func jsonPathPart(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func semanticIndex(number float64) string {
	if number == 0 {
		return "0"
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}

func (g *CredentialStreamGuard) WalkDelta(path string, value any, owner uint64) error {
	switch v := value.(type) {
	case string:
		for _, name := range []string{"role", "id", "type", "name"} {
			if strings.HasSuffix(path, "/"+name) {
				return nil
			}
		}
		if e := g.feed(path, v, owner); e != nil {
			return e
		}
		if strings.HasSuffix(path, "/arguments") {
			a := g.arguments[path]
			if a == nil {
				a = &argumentEscapes{}
				g.arguments[path] = a
			}
			return a.feed(g, path, v, owner)
		}
	case map[string]any:
		names := make([]string, 0, len(v))
		for name := range v {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if e := g.WalkDelta(path+"/"+jsonPathPart(name), v[name], owner); e != nil {
				return e
			}
		}
	case []any:
		for index, child := range v {
			identity := strconv.Itoa(index)
			if item, ok := child.(map[string]any); ok {
				if number, ok := item["index"].(float64); ok {
					identity = semanticIndex(number)
				}
			}
			if e := g.WalkDelta(path+"/"+identity, child, owner); e != nil {
				return e
			}
		}
	}
	return nil
}

func (g *CredentialStreamGuard) Push(raw string, emit func(string) error) error {
	if g.key == "" {
		return emit(raw)
	}
	var document map[string]any
	if json.Unmarshal([]byte(raw), &document) != nil {
		return Err(502, "invalid_upstream_response", "Provider sent invalid stream JSON")
	}
	if DecodedCredential(document, g.key) {
		return CredentialResponseError()
	}
	g.sequence++
	g.pending = append(g.pending, guardedChunk{g.sequence, raw})
	choices, _ := document["choices"].([]any)
	for _, entry := range choices {
		choice, _ := entry.(map[string]any)
		identity := "0"
		if number, ok := choice["index"].(float64); ok {
			identity = semanticIndex(number)
		}
		if e := g.WalkDelta("choice/"+identity, choice["delta"], g.sequence); e != nil {
			return e
		}
	}
	return g.flush(false, emit)
}

func (g *CredentialStreamGuard) flush(final bool, emit func(string) error) error {
	first := g.sequence + 1
	if !final {
		for _, m := range g.fields {
			if m.matched > 0 && m.owners[0] < first {
				first = m.owners[0]
			}
		}
		for _, a := range g.arguments {
			if owner := a.earliest(); owner > 0 && owner < first {
				first = owner
			}
		}
	}
	count := 0
	for count < len(g.pending) && g.pending[count].id < first {
		if e := emit(g.pending[count].raw); e != nil {
			return e
		}
		g.pending[count].raw = ""
		count++
	}
	g.pending = g.pending[count:]
	return nil
}

func (g *CredentialStreamGuard) Finish(emit func(string) error) error {
	for path, a := range g.arguments {
		if a.high != 0 {
			if e := g.feedDecoded(path, string(utf8.RuneError), a.highOwner); e != nil {
				return e
			}
			a.high = 0
		}
	}
	return g.flush(true, emit)
}
