package domain

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestReactionEmojiMatchesPinnedUnicodeData(t *testing.T) {
	file, err := os.Open("../../../../tools/unicode-emoji/data/17.0.0/emoji-test.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	accepted := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		points, rest, ok := strings.Cut(line, ";")
		if !ok {
			continue
		}
		status := strings.TrimSpace(strings.SplitN(rest, "#", 2)[0])
		var sequence strings.Builder
		for _, point := range strings.Fields(points) {
			codepoint, err := strconv.ParseInt(point, 16, 32)
			if err != nil {
				t.Fatal(err)
			}
			sequence.WriteRune(rune(codepoint))
		}
		value := sequence.String()
		if got := ValidateReactionEmoji(value) == nil; got != (status == "fully-qualified") {
			t.Fatalf("status=%s codepoints=%s accepted=%v", status, points, got)
		}
		if status == "fully-qualified" {
			accepted++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if accepted != 3944 || reactionEmojiVersion != "17.0" {
		t.Fatalf("incomplete pinned data: %s / %d", reactionEmojiVersion, accepted)
	}
}

func TestReactionEmojiRejectsUnsupportedSequences(t *testing.T) {
	for _, value := range []string{"♜️", "👍‍🔥", "🔥🏻", "🇦🇦", "❤", "1⃣", "🏻", "👍️", "👍👎", "", "ok", "\xff", strings.Repeat("👍", 17), "🏴\U000e0067\U000e0062\U000e0061\U000e0061\U000e0061\U000e007f"} {
		if ValidateReactionEmoji(value) == nil {
			t.Errorf("accepted unsupported sequence %q", value)
		}
	}
	for _, value := range []string{"🟢", "🟦", "❤️", "👍🏽", "👨‍💻", "1️⃣", "🇨🇳", "🏴\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f"} {
		if err := ValidateReactionEmoji(value); err != nil {
			t.Errorf("rejected fully-qualified sequence %q: %v", value, err)
		}
	}
}
