package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swarn007-byte/Relay/internal/config"
	"github.com/swarn007-byte/Relay/internal/security"
	"github.com/swarn007-byte/Relay/internal/storage"
	"github.com/swarn007-byte/Relay/internal/worker"
)

func TestSaaSTenantFlow(t *testing.T) {
	db, e := storage.Open(filepath.Join(t.TempDir(), "saas.db"))
	if upstream := os.Getenv("RELAY_TEST_LIBSQL_HTTP_URL"); upstream != "" {
		if e != nil {
			t.Fatal(e)
		}
		db.Close()
		target, err := url.Parse(upstream)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
		defer proxy.Close()
		previous := http.DefaultClient
		http.DefaultClient = proxy.Client()
		defer func() { http.DefaultClient = previous }()
		db, e = storage.OpenRemote(proxy.URL, "local-integration-test")
	}
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = storage.CreateTables(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	// Initialization must also work on a database that already has SaaS tables.
	if e = storage.CreateTables(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	guard := security.NewGuard(true, nil)
	srv := httptest.NewServer(New(&config.Config{SaaS: true, MaxBodyBytes: 1 << 20}, db, guard).Handler())
	defer srv.Close()
	for _, path := range []string{"/", "/app", "/app.js", "/app.css"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("asset %s: %d", path, res.StatusCode)
		}
	}
	newClient := func() *http.Client { jar, _ := cookiejar.New(nil); return &http.Client{Jar: jar} }
	alice, bob := newClient(), newClient()
	request := func(c *http.Client, method, path, body, key string, want int) map[string]any {
		t.Helper()
		r, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Relay-CSRF", "1")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		res, e := c.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, want, b)
		}
		out := map[string]any{}
		if e := json.Unmarshal(b, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	for i, c := range []*http.Client{alice, bob} {
		request(c, "POST", "/api/auth/signup", fmt.Sprintf(`{"email":"user%d@example.com","password":"long-secure-test-password"}`, i), "", 200)
	}
	p := request(alice, "POST", "/api/projects", `{"name":"Alice project"}`, "", 201)["id"].(string)
	base := "/api/projects/" + p
	request(bob, "GET", base+"/endpoints", "", "", 404)
	request(newClient(), "GET", base+"/keys", "", "", 401)
	// CSRF checks reject cookie-authenticated writes without a verification header.
	bad, _ := http.NewRequest("POST", srv.URL+"/api/projects", strings.NewReader(`{"name":"bad"}`))
	bad.Header.Set("Content-Type", "application/json")
	res, e := alice.Do(bad)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("missing CSRF check")
	}
	bad, _ = http.NewRequest("POST", srv.URL+"/api/projects", strings.NewReader(`{"name":"bad"}`))
	bad.Header.Set("Content-Type", "application/json")
	bad.Header.Set("X-Relay-CSRF", "1")
	bad.Header.Set("Origin", "https://attacker.example")
	res, e = alice.Do(bad)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-origin write accepted")
	}
	var signingSecret string
	received := make(chan error, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- worker.VerifySignature(b, []byte(signingSecret), r.Header.Get(worker.TimestampHeader), r.Header.Get(worker.SignatureHeader), time.Now(), time.Minute)
		w.WriteHeader(200)
	}))
	defer receiver.Close()
	endpoint := request(alice, "POST", base+"/endpoints", fmt.Sprintf(`{"name":"Receiver","url":%q}`, receiver.URL), "", 201)
	signingSecret = endpoint["signing_secret"].(string)
	k := request(alice, "POST", base+"/keys", `{"name":"Server"}`, "", 201)
	key := k["token"].(string)
	eventBody := fmt.Sprintf(`{"event":"payment.success","endpoint_id":%q,"payload":{"amount":500}}`, endpoint["id"])
	request(newClient(), "POST", "/events", eventBody, "", 401)
	// Another project's key cannot dispatch to Alice's endpoint.
	pb := request(bob, "POST", "/api/projects", `{"name":"Bob project"}`, "", 201)["id"].(string)
	kb := request(bob, "POST", "/api/projects/"+pb+"/keys", `{"name":"Bob key"}`, "", 201)["token"].(string)
	request(bob, "POST", "/api/v1/events", eventBody, kb, 404)
	accepted := request(alice, "POST", "/api/v1/events", eventBody, key, 202)
	delivery := accepted["delivery_id"].(string)
	request(bob, "GET", base+"/deliveries/"+delivery, "", "", 404)
	// Legacy global management routes must be unavailable in SaaS mode.
	request(alice, "GET", "/deliveries", "", "", 404)
	jobs, e := storage.FetchDueJobs(context.Background(), db, 10)
	if e != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%v err=%v", jobs, e)
	}
	if jobs[0].SigningSecret != signingSecret {
		t.Fatal("project signing secret not loaded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan storage.DeliveryJob, 1)
	wg := worker.StartPool(ctx, db, ch, 1, worker.NewClient(time.Second, guard, []byte("wrong-global-secret")), worker.Options{MaxAttempts: 2, LeaseTTL: time.Minute})
	claimed, e := storage.ClaimJob(ctx, db, delivery, "test-lease", time.Minute)
	if e != nil || !claimed {
		t.Fatal(e)
	}
	jobs[0].LeaseToken = "test-lease"
	ch <- jobs[0]
	close(ch)
	wg.Wait()
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("no signed delivery received")
	}
	detail := request(alice, "GET", base+"/deliveries/"+delivery, "", "", 200)
	if detail["delivery"].(map[string]any)["status"] != "delivered" {
		t.Fatal(detail)
	}
	if len(detail["attempts"].([]any)) != 1 {
		t.Fatal("attempt missing")
	}
	request(alice, "POST", base+"/deliveries/"+delivery+"/replay", "{}", "", 409)
	if _, e = db.Exec("update delivery_jobs set status='dead' where delivery_id=?", delivery); e != nil {
		t.Fatal(e)
	}
	request(alice, "POST", base+"/deliveries/"+delivery+"/replay", "{}", "", 202)
	request(alice, "POST", base+"/deliveries/"+delivery+"/replay", "{}", "", 409)
	usage := request(alice, "GET", base+"/usage", "", "", 200)
	if usage["events"].(float64) != 1 {
		t.Fatal(usage)
	}
	if _, e = db.Exec("update usage_months set events=10000 where project_id=?", p); e != nil {
		t.Fatal(e)
	}
	request(alice, "POST", "/api/v1/events", eventBody, key, 429)
	request(alice, "DELETE", base+"/keys/"+k["id"].(string), "{}", "", 200)
	request(alice, "POST", "/api/v1/events", eventBody, key, 401)
	request(alice, "DELETE", base+"/endpoints/"+endpoint["id"].(string), "{}", "", 200)
	fresh := request(alice, "POST", base+"/keys", `{"name":"New key"}`, "", 201)["token"].(string)
	request(alice, "POST", "/api/v1/events", eventBody, fresh, 404)
	request(alice, "POST", "/api/auth/logout", "{}", "", 200)
	request(alice, "GET", "/api/me", "", "", 401)
	request(alice, "POST", "/api/auth/login", `{"email":"user0@example.com","password":"long-secure-test-password"}`, "", 200)
	request(alice, "GET", base+"/keys", "", "", 200)
}

func TestProxyOriginProtection(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicURL: "https://relay.example.com"}}
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"https://relay.example.com", true},
		{"https://attacker.example", false},
		{"http://relay.example.com", false},
		{"https://backend.onrender.com", false},
	} {
		r := httptest.NewRequest("POST", "https://backend.onrender.com/api/projects", strings.NewReader(`{}`))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Relay-CSRF", "1")
		if got := s.safeWrite(httptest.NewRecorder(), r); got != tc.want {
			t.Errorf("origin %q got %v want %v", tc.origin, got, tc.want)
		}
	}
}
