package gateway

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"teamie/agent-infra/internal/api"
)

func credentialResponseError() error {
	return api.Err(502, "invalid_upstream_response", "Provider returned a credential in its response")
}

// Each semantic delta has its own incremental matcher. Owners identify the raw
// chunks containing the still-possible credential prefix, which must stay local.
// KMP fallback keeps only the suffix that can still complete a credential.
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
type credentialStreamGuard struct {
	key       string
	failure   []int
	sequence  uint64
	fields    map[credentialField]*credentialMatch
	arguments map[string]*argumentEscapes
	pending   []guardedChunk
}

func newCredentialStreamGuard(key string) *credentialStreamGuard {
	g := &credentialStreamGuard{key: key, fields: map[credentialField]*credentialMatch{}, arguments: map[string]*argumentEscapes{}}
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
func (g *credentialStreamGuard) feed(path, value string, owner uint64) error {
	return g.feedField(credentialField{path: path}, value, owner)
}
func (g *credentialStreamGuard) feedDecoded(path, value string, owner uint64) error {
	return g.feedField(credentialField{path: path, decoded: true}, value, owner)
}
func (g *credentialStreamGuard) feedField(field credentialField, value string, owner uint64) error {
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
				return credentialResponseError()
			}
		}
	}
	return nil
}

// Arguments are themselves JSON encoded inside the decoded delta string. This
// second incremental unescape pass also guards credentials written as \uXXXX,
// including escape sequences and surrogate pairs split between raw chunks.
// It never reconstructs a completed tool call or forwards a credential prefix.
type argumentEscapes struct {
	escape    string
	owner     uint64
	high      rune
	highOwner uint64
}

func (a *argumentEscapes) feed(g *credentialStreamGuard, path, value string, owner uint64) error {
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
				return api.Err(502, "invalid_upstream_response", "Provider sent invalid tool argument escapes")
			}
			if e = decoded(rune(unit), at); e != nil {
				return e
			}
			continue
		}
		var result string
		if json.Unmarshal([]byte(`"`+escaped+`"`), &result) != nil {
			return api.Err(502, "invalid_upstream_response", "Provider sent invalid tool argument escapes")
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

func decodedCredential(value any, key string) bool {
	if key == "" {
		return false
	}
	switch v := value.(type) {
	case string:
		return strings.Contains(v, key)
	case map[string]any:
		for name, child := range v {
			if strings.Contains(name, key) || decodedCredential(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if decodedCredential(child, key) {
				return true
			}
		}
	}
	return false
}
func guardSingleResponse(raw, key string) error {
	if key == "" {
		return nil
	}
	var document any
	if json.Unmarshal([]byte(raw), &document) != nil {
		return api.Err(502, "invalid_upstream_response", "Provider sent invalid JSON")
	}
	if decodedCredential(document, key) {
		return credentialResponseError()
	}
	g := newCredentialStreamGuard(key)
	if e := g.walkDelta("response", document, 1); e != nil {
		return e
	}
	return g.finish(func(string) error { return nil })
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
func (g *credentialStreamGuard) walkDelta(path string, value any, owner uint64) error {
	switch v := value.(type) {
	case string:
		// These fields are atomic protocol metadata, not appended text. Their
		// decoded whole value was checked already; a harmless trailing key
		// prefix must not hold every later content chunk until DONE.
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
			if e := g.walkDelta(path+"/"+jsonPathPart(name), v[name], owner); e != nil {
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
			if e := g.walkDelta(path+"/"+identity, child, owner); e != nil {
				return e
			}
		}
	}
	return nil
}
func (g *credentialStreamGuard) push(raw string, emit func(string) error) error {
	if g.key == "" {
		return emit(raw)
	}
	var document map[string]any
	if json.Unmarshal([]byte(raw), &document) != nil {
		return api.Err(502, "invalid_upstream_response", "Provider sent invalid stream JSON")
	}
	if decodedCredential(document, g.key) {
		return credentialResponseError()
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
		if e := g.walkDelta("choice/"+identity, choice["delta"], g.sequence); e != nil {
			return e
		}
	}
	return g.flush(false, emit)
}
func (g *credentialStreamGuard) flush(final bool, emit func(string) error) error {
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
func (g *credentialStreamGuard) finish(emit func(string) error) error {
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
