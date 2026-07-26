package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJsonOK_WritesStatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	jsonOK(rec, map[string]string{"hello": "world"})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("could not decode response body: %v", err)
	}
	if body["hello"] != "world" {
		t.Errorf("body = %v, want hello=world", body)
	}
}

func TestJsonError_WritesStatusAndErrorMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	jsonError(rec, "something broke", http.StatusBadRequest)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("could not decode response body: %v", err)
	}
	if body["error"] != "something broke" {
		t.Errorf("error message = %q, want %q", body["error"], "something broke")
	}
}

func TestPathSegment(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/deals/abc-123", nil)

	if got := pathSegment(req, 0); got != "api" {
		t.Errorf("segment 0 = %q, want %q", got, "api")
	}
	if got := pathSegment(req, 2); got != "abc-123" {
		t.Errorf("segment 2 = %q, want %q", got, "abc-123")
	}
	if got := pathSegment(req, 99); got != "" {
		t.Errorf("out-of-range segment = %q, want empty string", got)
	}
}

func TestUserFromRequest_UsesHeadersWhenPresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/deals", nil)
	req.Header.Set("X-User-ID", "user-1")
	req.Header.Set("X-User-Name", "Alice")

	id, name := userFromRequest(req)
	if id != "user-1" || name != "Alice" {
		t.Errorf("got id=%q name=%q, want id=user-1 name=Alice", id, name)
	}
}

func TestUserFromRequest_FallsBackWhenHeadersMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/deals", nil)

	id, name := userFromRequest(req)
	if id != "web-user" || name != "Web User" {
		t.Errorf("got id=%q name=%q, want fallback values", id, name)
	}
}
