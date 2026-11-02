package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Announcement represents a platform-wide announcement published by a platform administrator.
type Announcement struct {
	ID         string    `json:"id"`
	Seq        int64     `json:"seq"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	AuthorID   string    `json:"authorId"`
	AuthorName string    `json:"authorName"`
	CreatedAt  time.Time `json:"createdAt"`
}

// AnnouncementsPage contains a reverse-chronological slice of platform announcements
// together with the global latest sequence and the caller's read watermark.
type AnnouncementsPage struct {
	Items     []Announcement `json:"items"`
	LatestSeq int64          `json:"latestSeq"`
	ReadSeq   int64          `json:"readSeq"`
	HasMore   bool           `json:"hasMore"`
}

// ValidateAnnouncementInput trims and validates a candidate announcement title and Markdown body.
func ValidateAnnouncementInput(rawTitle, rawBody string) (title, body string, err error) {
	title = strings.TrimSpace(rawTitle)
	if title == "" || len([]rune(title)) > 120 || !utf8.ValidString(title) || strings.ContainsRune(title, '\x00') {
		return "", "", ErrInvalidAnnouncementTitle()
	}
	body = strings.TrimSpace(rawBody)
	if body == "" || len([]rune(body)) > 8000 || !utf8.ValidString(body) || strings.ContainsRune(body, '\x00') {
		return "", "", ErrInvalidAnnouncementBody()
	}
	return title, body, nil
}
