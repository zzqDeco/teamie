package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorMapsCodedErrorsAndRedactsUnknownErrors(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, Err(http.StatusForbidden, "forbidden", "Access denied"))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("unexpected coded error response: %d %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	WriteError(w2, errors.New("raw sql failure secret"))
	if w2.Code != http.StatusInternalServerError || strings.Contains(w2.Body.String(), "secret") {
		t.Fatalf("untrusted error text leaked: %d %s", w2.Code, w2.Body.String())
	}
}
