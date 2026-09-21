package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndOrderList(t *testing.T) {
	s := newServer()
	for _, path := range []string{"/healthz", "/api/orders"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s: got %d", path, res.Code)
		}
	}
}

func TestReviewRequiresNote(t *testing.T) {
	for _, note := range []string{"", " \t\n ", "　"} {
		s := newServer()
		before := *s.orders["ORD-1001"]
		body, _ := json.Marshal(reviewRequest{Decision: "APPROVED", Note: note})
		req := httptest.NewRequest(http.MethodPost, "/api/orders/ORD-1001/review", bytes.NewReader(body))
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("note %q: got %d, want 400", note, res.Code)
		}
		if *s.orders["ORD-1001"] != before {
			t.Fatalf("note %q changed the order", note)
		}
	}
}

func TestReviewRejectsInvalidJSON(t *testing.T) {
	s := newServer()
	req := httptest.NewRequest(http.MethodPost, "/api/orders/ORD-1001/review", bytes.NewBufferString("{"))
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", res.Code)
	}
}

func TestReviewRejectsTrailingJSON(t *testing.T) {
	for _, payload := range []string{
		`{"decision":"APPROVED","note":"valid prefix"}garbage`,
		`{"decision":"APPROVED","note":"first"}{"decision":"REJECTED","note":"second"}`,
	} {
		s := newServer()
		before := *s.orders["ORD-1001"]
		req := httptest.NewRequest(http.MethodPost, "/api/orders/ORD-1001/review", strings.NewReader(payload))
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("payload %q: got %d, want 400", payload, res.Code)
		}
		if *s.orders["ORD-1001"] != before {
			t.Fatalf("payload %q changed the order", payload)
		}
	}
}

func TestReviewRejectsUnsupportedDecision(t *testing.T) {

	s := newServer()
	body, _ := json.Marshal(reviewRequest{Decision: "PENDING", Note: "Needs another check"})
	req := httptest.NewRequest(http.MethodPost, "/api/orders/ORD-1001/review", bytes.NewReader(body))
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", res.Code)
	}
}

func TestReviewUpdatesOrder(t *testing.T) {
	s := newServer()
	body, _ := json.Marshal(reviewRequest{Decision: "APPROVED", Note: "Address confirmed with customer"})
	req := httptest.NewRequest(http.MethodPost, "/api/orders/ORD-1001/review", bytes.NewReader(body))
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", res.Code)
	}
	if s.orders["ORD-1001"].Status != "APPROVED" {
		t.Fatalf("status was not updated")
	}
	if s.orders["ORD-1001"].ReviewedAt == "" {
		t.Fatalf("review timestamp was not recorded")
	}
	var bodyResponse struct {
		Order      order  `json:"order"`
		ReviewedAt string `json:"reviewedAt"`
	}
	if err := json.NewDecoder(res.Body).Decode(&bodyResponse); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if bodyResponse.ReviewedAt == "" || bodyResponse.Order.ReviewedAt != bodyResponse.ReviewedAt {
		t.Fatalf("response timestamp was not consistent: %+v", bodyResponse)
	}
}

func TestReviewCanBeRepeated(t *testing.T) {
	s := newServer()
	for _, review := range []reviewRequest{
		{Decision: "APPROVED", Note: "Address confirmed"},
		{Decision: "REJECTED", Note: "Customer requested cancellation"},
	} {
		body, _ := json.Marshal(review)
		req := httptest.NewRequest(http.MethodPost, "/api/orders/ORD-1001/review", bytes.NewReader(body))
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", res.Code)
		}
	}
	if s.orders["ORD-1001"].Status != "REJECTED" {
		t.Fatalf("status was not replaced by the latest review")
	}
	if s.orders["ORD-1001"].Note != "Customer requested cancellation" {
		t.Fatalf("note was not replaced by the latest review")
	}
}

func TestReviewUnknownOrder(t *testing.T) {
	s := newServer()
	body, _ := json.Marshal(reviewRequest{Decision: "REJECTED", Note: "Insufficient information"})
	req := httptest.NewRequest(http.MethodPost, "/api/orders/ORD-9999/review", bytes.NewReader(body))
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", res.Code)
	}
}

// --- REQ-002: order exception evidence links ---------------------------------

func postEvidence(t *testing.T, s *server, orderID, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/orders/"+orderID+"/evidence", strings.NewReader(payload))
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	return res
}

func decodeError(t *testing.T, res *httptest.ResponseRecorder) string {
	t.Helper()
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("error response must be JSON, got Content-Type %q", got)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("error body must name the problem: %v", body)
	}
	return body["error"]
}

// AC-001: a reference needs a non-empty label and an http(s) URL; anything
// else returns 400 with a JSON error and nothing is stored.
func TestEvidenceRequiresLabelAndHTTPURL(t *testing.T) {
	for _, payload := range []string{
		`{"label":"","url":"https://tickets.example/T-1"}`,
		`{"label":"   ","url":"https://tickets.example/T-1"}`,
		`{"url":"https://tickets.example/T-1"}`,
		`{"label":"ticket","url":""}`,
		`{"label":"ticket"}`,
		`{"label":"ticket","url":"tickets.example/T-1"}`,
		`{"label":"ticket","url":"ftp://files.example/T-1"}`,
		`{"label":"ticket","url":"javascript:alert(1)"}`,
		`{"label":"ticket","url":"https://"}`,
		`{"label":"ticket","url":"https://tickets.example/T-1"}garbage`,
		`{`,
	} {
		s := newServer()
		res := postEvidence(t, s, "ORD-1001", payload)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("payload %s: got %d, want 400", payload, res.Code)
		}
		decodeError(t, res)
		if len(s.evidence["ORD-1001"]) != 0 {
			t.Fatalf("payload %s stored a reference", payload)
		}
	}
}

// AC-002: the fourth reference on the same order returns 409; the first three
// are kept in order.
func TestEvidenceIsCappedAtThreePerOrder(t *testing.T) {
	s := newServer()
	for i := 1; i <= 3; i++ {
		res := postEvidence(t, s, "ORD-1001", `{"label":"ref `+strings.Repeat("x", i)+`","url":"https://tickets.example/T-`+strings.Repeat("1", i)+`"}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("reference %d: got %d, want 201: %s", i, res.Code, res.Body.String())
		}
	}
	res := postEvidence(t, s, "ORD-1001", `{"label":"one too many","url":"https://tickets.example/T-4"}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("fourth reference: got %d, want 409", res.Code)
	}
	decodeError(t, res)
	if got := len(s.evidence["ORD-1001"]); got != 3 {
		t.Fatalf("order must keep exactly three references, has %d", got)
	}
	if s.evidence["ORD-1001"][2].URL != "https://tickets.example/T-111" {
		t.Fatalf("references must keep insertion order: %+v", s.evidence["ORD-1001"])
	}
	// Another order is unaffected by the cap.
	if res := postEvidence(t, s, "ORD-1002", `{"label":"chat","url":"http://chat.example/thread/9"}`); res.Code != http.StatusCreated {
		t.Fatalf("other order: got %d, want 201", res.Code)
	}
}

// AC-003: unknown order returns 404.
func TestEvidenceUnknownOrder(t *testing.T) {
	s := newServer()
	res := postEvidence(t, s, "ORD-9999", `{"label":"ticket","url":"https://tickets.example/T-1"}`)
	if res.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", res.Code)
	}
	decodeError(t, res)
	if len(s.evidence) != 0 {
		t.Fatalf("nothing may be stored for an unknown order: %v", s.evidence)
	}
}

// AC-004: references appear in GET /api/orders next to their order (and only
// there); orders without references carry an empty array.
func TestEvidenceIsListedWithTheOrder(t *testing.T) {
	s := newServer()
	if res := postEvidence(t, s, "ORD-1002", `{"label":"  Ticket T-7  ","url":" https://tickets.example/T-7 "}`); res.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201: %s", res.Code, res.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	var orders []struct {
		ID       string              `json:"id"`
		Status   string              `json:"status"`
		Evidence []evidenceReference `json:"evidence"`
	}
	if err := json.NewDecoder(res.Body).Decode(&orders); err != nil {
		t.Fatalf("decode orders: %v", err)
	}
	seen := map[string]int{}
	for _, item := range orders {
		seen[item.ID] = len(item.Evidence)
		if item.Evidence == nil {
			t.Fatalf("%s must carry an evidence array even when empty", item.ID)
		}
		if item.ID == "ORD-1002" {
			if item.Evidence[0].Label != "Ticket T-7" || item.Evidence[0].URL != "https://tickets.example/T-7" || item.Evidence[0].AddedAt == "" {
				t.Fatalf("reference was not kept as trimmed input with a timestamp: %+v", item.Evidence[0])
			}
		}
	}
	if seen["ORD-1002"] != 1 || seen["ORD-1001"] != 0 {
		t.Fatalf("references must be listed only with their order: %v", seen)
	}
}

// The unknown action under /api/orders/{id}/ stays a 404 and the review
// endpoint is unchanged by the new dispatcher (AC-005).
func TestUnknownOrderActionIsNotFound(t *testing.T) {
	s := newServer()
	req := httptest.NewRequest(http.MethodPost, "/api/orders/ORD-1001/attachments", strings.NewReader(`{}`))
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", res.Code)
	}
}
