package domain

// ToolObservationReceipt represents a durable tool observation snapshot persisted
// under a worker lease fence so replayed turns preserve stable tool results.
type ToolObservationReceipt struct {
	RunID          string
	CallID         string
	ObservationKey string
	Response       []byte
}

// Valid reports whether the tool observation receipt has non-empty identifiers.
func (r ToolObservationReceipt) Valid() bool {
	return ValidUUID(r.RunID) && r.CallID != "" && r.ObservationKey != ""
}

// ToolRequestHash computes the deterministic SHA-256 request hash for a tool call.
func ToolRequestHash(name, rawArgs string) string {
	return Digest([]byte(name + "\x00" + rawArgs))
}
