package control

import (
	"encoding/json"
	"os"
)

type Flags struct{ Path string }

func (f Flags) Enabled(key string) bool {
	if f.Path == "" {
		return false
	}
	b, e := os.ReadFile(f.Path)
	if e != nil {
		return false
	}
	var v struct {
		Revision string          `json:"revision"`
		Flags    map[string]bool `json:"flags"`
	}
	if json.Unmarshal(b, &v) != nil || v.Revision == "" {
		return false
	}
	return v.Flags[key]
}
