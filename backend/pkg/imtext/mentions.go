// Package imtext defines generic IM message annotations without runtime types.
package imtext

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Mention identifies a recipient, never the sender or an authorization grant.
// Start and End are half-open UTF-16 offsets into the original message body.
type Mention struct {
	UserID string `json:"userId"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

func ValidateMentions(body string, mentions []Mention) error {
	if len(mentions) > 32 || !utf8.ValidString(body) {
		return errors.New("invalid mentions")
	}
	boundaries := map[int]int{0: 0}
	units := 0
	for at, r := range body {
		boundaries[units] = at
		units++
		if r > 0xffff {
			units++
		}
	}
	boundaries[units] = len(body)
	end := 0
	for _, m := range mentions {
		startByte, startOK := boundaries[m.Start]
		endByte, endOK := boundaries[m.End]
		if m.UserID == "" || len(m.UserID) > 128 || m.Start < end || m.End <= m.Start || !startOK || !endOK {
			return errors.New("invalid mention range")
		}
		text := body[startByte:endByte]
		if !strings.HasPrefix(text, "@") || utf8.RuneCountInString(text) < 2 || utf8.RuneCountInString(text) > 181 || strings.ContainsAny(text, "\r\n\t") {
			return errors.New("invalid mention text")
		}
		end = m.End
	}
	return nil
}
