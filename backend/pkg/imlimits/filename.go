package imlimits

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func ValidFilename(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || len([]rune(name)) > 180 || len(name) > 512 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}
