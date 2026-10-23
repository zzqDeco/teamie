package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	AttachmentBytes             = 4 * 1024 * 1024
	AttachmentsPerMessage       = 8
	PendingAttachmentsPerSender = 32
	AttachmentRequestBytes      = AttachmentBytes + 64*1024
)

type Attachment struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	MediaType string `json:"mediaType"`
	SizeBytes int    `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type AttachmentUpload struct {
	Conversation string
	Fingerprint  string
	Attachment   Attachment
	Available    bool
}

func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func UploadFingerprint(conversation, filename, contentHash string) string {
	return HashString(conversation + "\x00" + filename + "\x00" + contentHash)
}

func ValidUploadKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

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

func ValidateFilename(name string) error {
	if !ValidFilename(name) {
		return ErrInvalidFilename()
	}
	return nil
}

func ValidateAttachmentIDs(ids []string) error {
	if len(ids) > AttachmentsPerMessage {
		return ErrTooManyAttachments()
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !BoundedString(id, 1, 128) || seen[id] {
			return ErrDuplicateAttachmentID()
		}
		seen[id] = true
	}
	return nil
}
