// Package fs provides attachment MIME sniffing and file-content inspection helpers
// for the Chat service.
package fs

import (
	"net/http"
)

// SniffMediaType detects the MIME type of an uploaded file byte stream.
func SniffMediaType(content []byte) string {
	if len(content) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(content)
}
