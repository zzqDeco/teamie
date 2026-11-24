package domain

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseSearchTerms(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "empty", raw: "   \t\n ", want: nil},
		{name: "single_word", raw: "  Release ", want: []string{"Release"}},
		{name: "multi_word_and_cjk", raw: "发布 v0.4.11 失败", want: []string{"发布", "v0.4.11", "失败"}},
		{name: "quoted_phrase", raw: `"error code" 500`, want: []string{"error code", "500"}},
		{name: "empty_quotes_skipped", raw: `""   "   " alpha`, want: []string{"alpha"}},
		{name: "unclosed_quote_kept_literal", raw: `"unclosed phrase`, want: []string{`"unclosed`, "phrase"}},
		{name: "dedup_case_insensitive", raw: `Alpha alpha "ALPHA" beta`, want: []string{"Alpha", "beta"}},
		{name: "like_metachars_preserved", raw: `100% foo_bar a\b`, want: []string{"100%", "foo_bar", `a\b`}},
		{name: "max_8_terms_ok", raw: "t1 t2 t3 t4 t5 t6 t7 t8", want: []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}},
		{name: "9_distinct_terms_rejected", raw: "t1 t2 t3 t4 t5 t6 t7 t8 t9", wantErr: true},
		{name: "9_with_duplicate_ok", raw: "t1 t2 t3 t4 t5 t6 t7 t8 T1", want: []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}},
		{name: "over_200_runes_rejected", raw: strings.Repeat("测", 201), wantErr: true},
		{name: "exact_200_runes_ok", raw: strings.Repeat("测", 200), want: []string{strings.Repeat("测", 200)}},
		{name: "nul_byte_rejected", raw: "abc\x00def", wantErr: true},
		{name: "invalid_utf8_rejected", raw: string([]byte{0xff, 0xfe}), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSearchTerms(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.raw, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ParseSearchTerms(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestValidateGlobalSearchQuery(t *testing.T) {
	// Empty query with no filters is rejected.
	if _, err := ValidateGlobalSearchQuery("", "", "", "", "", "", "", "", 20); err == nil {
		t.Fatal("expected error when query and filters are all empty")
	}
	// Only workspaceId/conversationId without any search criterion is also rejected.
	if _, err := ValidateGlobalSearchQuery(`""`, "ws1", "c1", "", "false", "", "", "", 20); err == nil {
		t.Fatal("expected error when only scope IDs are supplied")
	}
	// Filter-only queries (senderId, hasFiles=true, after/before) are allowed.
	q, err := ValidateGlobalSearchQuery("", "ws1", "", "u1", "true", "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z", "", 25)
	if err != nil {
		t.Fatalf("unexpected error for filter-only query: %v", err)
	}
	if q.WorkspaceID != "ws1" || q.SenderID != "u1" || !q.HasFiles || q.After == nil || q.Before == nil || q.Limit != 25 {
		t.Fatalf("unexpected parsed query: %+v", q)
	}
	// Inverted time bounds rejected.
	if _, err := ValidateGlobalSearchQuery("hello", "", "", "", "", "2026-03-01T00:00:00Z", "2026-02-01T00:00:00Z", "", 20); err == nil {
		t.Fatal("expected error for inverted after/before")
	}
	// Equal timestamps allowed.
	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := ValidateGlobalSearchQuery("", "", "", "", "", ts, ts, "", 20); err != nil {
		t.Fatalf("equal after/before should be valid: %v", err)
	}
	// Invalid hasFiles, limit, timestamp, cursor rejected.
	if _, err := ValidateGlobalSearchQuery("hi", "", "", "", "yes", "", "", "", 20); err == nil {
		t.Fatal("expected error for invalid hasFiles")
	}
	if _, err := ValidateGlobalSearchQuery("hi", "", "", "", "", "not-a-time", "", "", 20); err == nil {
		t.Fatal("expected error for invalid after timestamp")
	}
	if _, err := ValidateGlobalSearchQuery("hi", "", "", "", "", "", "", "bad-cursor", 20); err == nil {
		t.Fatal("expected error for invalid cursor")
	}
	if _, err := ValidateGlobalSearchQuery("hi", "", "", "", "", "", "", "", 0); err == nil {
		t.Fatal("expected error for limit 0")
	}
	if _, err := ValidateGlobalSearchQuery("hi", "", "", "", "", "", "", "", 51); err == nil {
		t.Fatal("expected error for limit 51")
	}
}

func TestValidateAttachmentGalleryQueryAndCursor(t *testing.T) {
	encoded, err := EncodeAttachmentGalleryCursor(42, "att-123")
	if err != nil {
		t.Fatalf("EncodeAttachmentGalleryCursor failed: %v", err)
	}
	q, err := ValidateAttachmentGalleryQuery("", " report.pdf ", encoded, 40)
	if err != nil {
		t.Fatalf("ValidateAttachmentGalleryQuery failed: %v", err)
	}
	if q.Kind != "all" || q.Query != "report.pdf" || !q.HasCursor || q.Cursor.Seq != 42 || q.Cursor.ID != "att-123" || q.Limit != 40 {
		t.Fatalf("unexpected gallery query: %+v", q)
	}
	for _, kind := range []string{"all", "image", "file"} {
		if _, err := ValidateAttachmentGalleryQuery(kind, "", "", 60); err != nil {
			t.Fatalf("kind %q should be valid: %v", kind, err)
		}
	}
	if _, err := ValidateAttachmentGalleryQuery("video", "", "", 40); err == nil {
		t.Fatal("expected error for unsupported kind")
	}
	if _, err := ValidateAttachmentGalleryQuery("all", "", "not-base64!", 40); err == nil {
		t.Fatal("expected error for bad gallery cursor")
	}
	if _, err := ValidateAttachmentGalleryQuery("all", "", "", 61); err == nil {
		t.Fatal("expected error for limit > 60")
	}
	if _, err := ValidateAttachmentGalleryQuery("all", strings.Repeat("a", 201), "", 20); err == nil {
		t.Fatal("expected error for overlong filename query")
	}
}
