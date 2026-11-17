package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/swarn007-byte/Relay/internal/config"
	"github.com/swarn007-byte/Relay/internal/security"
	"github.com/swarn007-byte/Relay/internal/storage"
	_ "modernc.org/sqlite"
)

const testAdminToken = "test-admin-token-123456"

func newTestAPI(t *testing.T, allowPrivate bool, adminToken string) (*httptest.Server, *sql.DB) {
	t.Helper()

	db, err := storage.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.CreateTables(context.Background(), db); err != nil {
		t.Fatalf("create tables: %v", err)
	}

	cfg := &config.Config{
		MaxBodyBytes: 1 << 20,
		AdminToken:   adminToken,
	}
	guard := security.NewGuard(allowPrivate, nil)
	srv := httptest.NewServer(New(cfg, db, guard).Handler())
	t.Cleanup(srv.Close)

	return srv, db
}

func doRequest(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(payload)
}

func postEvent(t *testing.T, srv *httptest.Server, body, contentType string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", srv.URL+"/events", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(payload)
}

func decodeEventResponse(t *testing.T, body string) (string, string) {
	t.Helper()
	var parsed struct {
		EventID    string `json:"event_id"`
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("invalid event response %q: %v", body, err)
	}
	if parsed.EventID == "" || parsed.DeliveryID == "" {
		t.Fatalf("missing ids in response: %q", body)
	}
	return parsed.EventID, parsed.DeliveryID
}

func TestEventValidation(t *testing.T) {
	validBody := `{"event":"payment.success","target_url":"https://example.com/hook","payload":{"amount":500}}`

	cases := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantError   string
	}{
		{"valid event", validBody, "application/json", http.StatusAccepted, ""},
		{"missing event", `{"target_url":"https://example.com/hook","payload":{}}`, "application/json", http.StatusBadRequest, "event is required"},
		{"missing target", `{"event":"x","payload":{}}`, "application/json", http.StatusBadRequest, "target_url is required"},
		{"empty target", `{"event":"x","target_url":"  ","payload":{}}`, "application/json", http.StatusBadRequest, "target_url is required"},
		{"bad scheme", `{"event":"x","target_url":"ftp://example.com/hook","payload":{}}`, "application/json", http.StatusBadRequest, "scheme"},
		{"missing payload", `{"event":"x","target_url":"https://example.com/hook"}`, "application/json", http.StatusBadRequest, "payload is required"},
		{"null payload", `{"event":"x","target_url":"https://example.com/hook","payload":null}`, "application/json", http.StatusBadRequest, "payload must not be null"},
		{"invalid json", `{"event":`, "application/json", http.StatusBadRequest, "invalid JSON"},
		{"trailing json", validBody + `{"event":"y"}`, "application/json", http.StatusBadRequest, "single JSON"},
		{"wrong content type", validBody, "text/plain", http.StatusUnsupportedMediaType, "Content-Type"},
		{"missing content type", validBody, "", http.StatusUnsupportedMediaType, "Content-Type"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newTestAPI(t, true, testAdminToken)
			status, body := postEvent(t, srv, tc.body, tc.contentType)
			if status != tc.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tc.wantStatus, status, body)
			}
			if tc.wantError != "" && !strings.Contains(body, tc.wantError) {
				t.Fatalf("expected error containing %q, got %s", tc.wantError, body)
			}

			var eventCount, jobCount int
			if err := db.QueryRow(`select count(*) from events`).Scan(&eventCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`select count(*) from delivery_jobs`).Scan(&jobCount); err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus == http.StatusAccepted {
				if eventCount != 1 || jobCount != 1 {
					t.Fatalf("expected 1 event and 1 job, got %d/%d", eventCount, jobCount)
				}
			} else if eventCount != 0 || jobCount != 0 {
				t.Fatalf("rejected event was persisted: %d/%d", eventCount, jobCount)
			}
		})
	}
}

func TestEventAcceptedResponse(t *testing.T) {
	srv, db := newTestAPI(t, true, testAdminToken)

	body := `{"event":"payment.success","target_url":"https://example.com/hook","payload":{"amount":500}}`
	status, resp := postEvent(t, srv, body, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", status, resp)
	}
	eventID, deliveryID := decodeEventResponse(t, resp)

	var storedPayload string
	var jobStatus string
	err := db.QueryRow(
		`select e.payload, d.status from delivery_jobs d join events e on d.event_id = e.id
		 where d.delivery_id = ?`, deliveryID,
	).Scan(&storedPayload, &jobStatus)
	if err != nil {
		t.Fatal(err)
	}
	if storedPayload != `{"amount":500}` {
		t.Fatalf("payload not stored exactly: %q", storedPayload)
	}
	if jobStatus != "pending" {
		t.Fatalf("expected pending job, got %s", jobStatus)
	}

	var storedEventID string
	if err := db.QueryRow(`select id from events where id = ?`, eventID).Scan(&storedEventID); err != nil {
		t.Fatalf("event not persisted: %v", err)
	}
}

func TestEventRejectsPrivateTarget(t *testing.T) {
	srv, _ := newTestAPI(t, false, testAdminToken)

	body := `{"event":"x","target_url":"http://169.254.169.254/latest/meta-data/","payload":{}}`
	status, resp := postEvent(t, srv, body, "application/json")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", status, resp)
	}
	if !strings.Contains(resp, "not allowed") {
		t.Fatalf("unexpected error: %s", resp)
	}
}

func TestEventBodySizeLimit(t *testing.T) {
	srv, _ := newTestAPI(t, true, testAdminToken)

	huge := `{"event":"x","target_url":"https://example.com/hook","payload":"` +
		strings.Repeat("a", 1<<20) + `"}`
	status, _ := postEvent(t, srv, huge, "application/json")
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", status)
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv, _ := newTestAPI(t, true, testAdminToken)
	status, body := doRequest(t, "GET", srv.URL+"/health", "", "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if !strings.Contains(body, "ok") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestAdminAuthorization(t *testing.T) {
	t.Run("disabled without token", func(t *testing.T) {
		srv, _ := newTestAPI(t, true, "")
		status, body := doRequest(t, "GET", srv.URL+"/deliveries", "", "")
		if status != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", status, body)
		}
	})

	srv, _ := newTestAPI(t, true, testAdminToken)

	t.Run("missing header", func(t *testing.T) {
		status, _ := doRequest(t, "GET", srv.URL+"/deliveries", "", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", status)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		status, _ := doRequest(t, "GET", srv.URL+"/deliveries", "wrong-token-123456", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", status)
		}
	})

	t.Run("valid token", func(t *testing.T) {
		status, _ := doRequest(t, "GET", srv.URL+"/deliveries", testAdminToken, "")
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
	})

	t.Run("replay requires auth", func(t *testing.T) {
		id := uuid.NewString()
		status, _ := doRequest(t, "POST", srv.URL+"/deliveries/"+id+"/replay", "", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", status)
		}
	})
}

func seedDeadDelivery(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	eventID := uuid.NewString()
	deliveryID, err := storage.SaveEvent(ctx, db, eventID, "payment.failed", "https://example.com/hook", []byte(`{"amount":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`update delivery_jobs set status = 'dead', attempts = 5, next_attempt_at = null
		 where delivery_id = ?`, deliveryID); err != nil {
		t.Fatal(err)
	}
	return deliveryID
}

func TestDeliveryInspectionAndReplay(t *testing.T) {
	srv, db := newTestAPI(t, true, testAdminToken)

	body := `{"event":"payment.success","target_url":"https://example.com/hook","payload":{"amount":500}}`
	status, resp := postEvent(t, srv, body, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", status, resp)
	}
	_, deliveryID := decodeEventResponse(t, resp)

	status, detail := doRequest(t, "GET", srv.URL+"/deliveries/"+deliveryID, testAdminToken, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, detail)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["status"] != "pending" {
		t.Fatalf("unexpected status: %v", parsed["status"])
	}
	if parsed["event_type"] != "payment.success" {
		t.Fatalf("unexpected event type: %v", parsed["event_type"])
	}
	if parsed["payload"] != `{"amount":500}` {
		t.Fatalf("unexpected payload: %v", parsed["payload"])
	}

	status, attemptsBody := doRequest(t, "GET", srv.URL+"/deliveries/"+deliveryID+"/attempts", testAdminToken, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, attemptsBody)
	}
	if !strings.Contains(attemptsBody, `"attempts":[]`) {
		t.Fatalf("expected empty attempts, got %s", attemptsBody)
	}

	deadID := seedDeadDelivery(t, db)

	status, listBody := doRequest(t, "GET", srv.URL+"/deliveries?status=dead", testAdminToken, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, listBody)
	}
	if !strings.Contains(listBody, deadID) {
		t.Fatalf("dead delivery missing from list: %s", listBody)
	}
	if strings.Contains(listBody, deliveryID) {
		t.Fatalf("pending delivery leaked into dead list: %s", listBody)
	}

	status, replayBody := doRequest(t, "POST", srv.URL+"/deliveries/"+deadID+"/replay", testAdminToken, "")
	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", status, replayBody)
	}
	var replay struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal([]byte(replayBody), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.DeliveryID == "" || replay.DeliveryID == deadID {
		t.Fatalf("unexpected replay response: %s", replayBody)
	}

	status, newDetail := doRequest(t, "GET", srv.URL+"/deliveries/"+replay.DeliveryID, testAdminToken, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, newDetail)
	}
	var newParsed map[string]interface{}
	if err := json.Unmarshal([]byte(newDetail), &newParsed); err != nil {
		t.Fatal(err)
	}
	if newParsed["status"] != "pending" {
		t.Fatalf("replay not pending: %v", newParsed["status"])
	}

	status, _ = doRequest(t, "GET", srv.URL+"/deliveries/"+deadID, testAdminToken, "")
	if status != http.StatusOK {
		t.Fatalf("original delivery should stay inspectable, got %d", status)
	}

	status, conflictBody := doRequest(t, "POST", srv.URL+"/deliveries/"+deadID+"/replay", testAdminToken, "")
	if status != http.StatusConflict {
		t.Fatalf("expected 409 while replay is active, got %d: %s", status, conflictBody)
	}

	status, _ = doRequest(t, "POST", srv.URL+"/deliveries/"+replay.DeliveryID+"/replay", testAdminToken, "")
	if status != http.StatusConflict {
		t.Fatalf("expected 409 for non-dead replay, got %d", status)
	}
}

func TestDeliveryIdentifierValidation(t *testing.T) {
	srv, _ := newTestAPI(t, true, testAdminToken)

	status, _ := doRequest(t, "GET", srv.URL+"/deliveries/not-a-uuid", testAdminToken, "")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed id, got %d", status)
	}

	status, body := doRequest(t, "GET", srv.URL+"/deliveries/"+uuid.NewString(), testAdminToken, "")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown id, got %d: %s", status, body)
	}
}

func TestDeliveryListValidation(t *testing.T) {
	srv, _ := newTestAPI(t, true, testAdminToken)

	status, _ := doRequest(t, "GET", srv.URL+"/deliveries?status=bogus", testAdminToken, "")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad status, got %d", status)
	}

	status, _ = doRequest(t, "GET", srv.URL+"/deliveries?limit=0", testAdminToken, "")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad limit, got %d", status)
	}

	status, _ = doRequest(t, "GET", srv.URL+"/deliveries?limit=999", testAdminToken, "")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for out-of-range limit, got %d", status)
	}

	status, _ = doRequest(t, "GET", srv.URL+"/deliveries", testAdminToken, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200 for empty list, got %d", status)
	}
}

func TestUnknownEndpointReturnsJSON(t *testing.T) {
	srv, _ := newTestAPI(t, true, testAdminToken)
	status, body := doRequest(t, "GET", srv.URL+"/nope", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
	if !strings.Contains(body, `"error"`) {
		t.Fatalf("expected JSON error, got %s", body)
	}
}

func TestEventTimestampsAreDurable(t *testing.T) {
	srv, db := newTestAPI(t, true, testAdminToken)

	body := `{"event":"x","target_url":"https://example.com/hook","payload":{}}`
	status, resp := postEvent(t, srv, body, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", status, resp)
	}
	_, deliveryID := decodeEventResponse(t, resp)

	var nextType string
	if err := db.QueryRow(
		`select typeof(next_attempt_at) from delivery_jobs where delivery_id = ?`,
		deliveryID,
	).Scan(&nextType); err != nil {
		t.Fatal(err)
	}
	if nextType != "integer" {
		t.Fatalf("expected integer timestamp, got %s", nextType)
	}

	var nextAt int64
	if err := db.QueryRow(
		`select next_attempt_at from delivery_jobs where delivery_id = ?`,
		deliveryID,
	).Scan(&nextAt); err != nil {
		t.Fatal(err)
	}
	if time.UnixMilli(nextAt).After(time.Now().Add(time.Minute)) {
		t.Fatalf("job not immediately due: %d", nextAt)
	}
}
