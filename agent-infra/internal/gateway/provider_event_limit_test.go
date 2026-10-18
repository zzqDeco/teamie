package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type countedEventReader struct {
	io.Reader
	read int
}

func (r *countedEventReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestMultilineSSEEventIsBoundedBeforeDelimiter(t *testing.T) {
	line := "data: " + strings.Repeat(" ", 4<<20) + "\n"
	source := &countedEventReader{Reader: io.MultiReader(strings.NewReader(line), strings.NewReader(line), strings.NewReader(line), strings.NewReader("\n"))}
	response := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(source)}
	guarded, err := guardStreamingResponse(response, "synthetic-key", context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer guarded.Body.Close()
	_, err = io.Copy(io.Discard, guarded.Body)
	if guardErrorCode(err) != "invalid_upstream_response" || source.read >= 3*len(line) {
		t.Fatal("oversized multiline event was retained until the delimiter", source.read, err)
	}
}

func TestValidMultilineSSEEventStillReachesTheReader(t *testing.T) {
	chunk := guardChunk(map[string]any{"content": "small valid content"})
	cut := strings.Index(chunk, ",")
	body := "data: " + chunk[:cut+1] + "\ndata: " + chunk[cut+1:] + "\n\ndata: " + finalDelta + "\n\ndata: [DONE]\n\n"
	response := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	guarded, err := guardStreamingResponse(response, "synthetic-key", context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer guarded.Body.Close()
	raw, err := io.ReadAll(guarded.Body)
	if err != nil || !strings.Contains(string(raw), "small valid content") || !strings.Contains(string(raw), "[DONE]") {
		t.Fatal("valid multiline event was rejected", err)
	}
}
