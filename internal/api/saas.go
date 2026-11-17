package api

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/swarn007-byte/Relay/internal/storage"
)

const monthlyLimit = 10000

func token() string { return hex.EncodeToString(randomBytes(32)) }
func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func passwordHash(password, salt string) string {
	b, err := pbkdf2.Key(sha256.New, password, []byte(salt), 210000, 32)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (s *Server) saasRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/signup", s.authenticate)
	mux.HandleFunc("POST /api/auth/login", s.authenticate)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("/api/projects", s.projects)
	mux.HandleFunc("/api/projects/{project}/{resource}", s.projectResource)
	mux.HandleFunc("/api/projects/{project}/{resource}/{id}", s.projectResource)
	mux.HandleFunc("POST /api/projects/{project}/deliveries/{id}/replay", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("resource", "deliveries")
		s.projectResource(w, r)
	})
	mux.HandleFunc("POST /api/v1/events", s.ingest)
	mux.HandleFunc("POST /events", s.ingest)
}
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	limit := s.cfg.MaxBodyBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, 415, "application/json required")
		return false
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "invalid request body")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "single JSON object required")
		return false
	}
	return true
}
func (s *Server) safeWrite(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" {
		return true
	}
	if r.Header.Get("X-Relay-CSRF") != "1" {
		writeError(w, 403, "missing request verification header")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		expected := r.Host
		if s.cfg.PublicURL != "" {
			public, _ := url.Parse(s.cfg.PublicURL)
			expected = public.Host
			if e == nil && u.Scheme != public.Scheme {
				writeError(w, 403, "origin rejected")
				return false
			}
		}
		if e != nil || u.Host != expected {
			writeError(w, 403, "origin rejected")
			return false
		}
	}
	return true
}
func (s *Server) account(r *http.Request) (string, error) {
	c, e := r.Cookie("relay_session")
	if e != nil {
		return "", e
	}
	var id string
	e = s.db.QueryRowContext(r.Context(), "select account_id from sessions where token_hash=? and expires_at>?", digest(c.Value), time.Now().Unix()).Scan(&id)
	return id, e
}
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.safeWrite(w, r) {
		return "", false
	}
	id, e := s.account(r)
	if e != nil {
		writeError(w, 401, "please sign in")
		return "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	return id, true
}
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) {
	if !s.safeWrite(w, r) {
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	bucket := "auth:" + host
	var attempts int
	err := s.db.QueryRowContext(r.Context(), `insert into auth_limits(bucket,attempts,expires_at) values(?,1,?) on conflict(bucket) do update set attempts=case when expires_at < ? then 1 else attempts+1 end, expires_at=case when expires_at < ? then excluded.expires_at else expires_at end returning attempts`, bucket, time.Now().Add(15*time.Minute).Unix(), time.Now().Unix(), time.Now().Unix()).Scan(&attempts)
	if err != nil {
		writeError(w, 503, "authentication unavailable")
		return
	}
	if attempts > 30 {
		w.Header().Set("Retry-After", "900")
		writeError(w, 429, "too many attempts; try again later")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	addr, e := mail.ParseAddress(req.Email)
	if e != nil || addr.Address != req.Email || len(req.Email) > 254 || len(req.Password) < 12 || len(req.Password) > 256 {
		writeError(w, 400, "use a valid email and a password of 12–256 characters")
		return
	}
	var id, hash, salt string
	if strings.HasSuffix(r.URL.Path, "signup") {
		id = uuid.NewString()
		salt = token()
		hash = passwordHash(req.Password, salt)
		_, e = s.db.ExecContext(r.Context(), "insert into accounts values(?,?,?,?,?)", id, req.Email, hash, salt, time.Now().Unix())
		if e != nil {
			writeError(w, 409, "account could not be created; try signing in")
			return
		}
	} else {
		e = s.db.QueryRowContext(r.Context(), "select id,password_hash,salt from accounts where email=?", req.Email).Scan(&id, &hash, &salt)
		if e != nil {
			salt = "dummy-authentication-salt"
			hash = ""
		}
		valid := constantTimeEqual(passwordHash(req.Password, salt), hash)
		if e != nil || !valid {
			writeError(w, 401, "email or password is incorrect")
			return
		}
	}
	raw := token()
	_, e = s.db.ExecContext(r.Context(), "insert into sessions values(?,?,?)", digest(raw), id, time.Now().Add(7*24*time.Hour).Unix())
	if e != nil {
		writeError(w, 500, "session creation failed")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: raw, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicURL, "https://") || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteStrictMode, MaxAge: 604800})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"email": req.Email})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.safeWrite(w, r) {
		return
	}
	if c, e := r.Cookie("relay_session"); e == nil {
		if _, e = s.db.ExecContext(r.Context(), "delete from sessions where token_hash=?", digest(c.Value)); e != nil {
			writeError(w, 500, "logout failed")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r)
	if !ok {
		return
	}
	var email string
	if e := s.db.QueryRowContext(r.Context(), "select email from accounts where id=?", id).Scan(&email); e != nil {
		writeError(w, 500, "account unavailable")
		return
	}
	writeJSON(w, 200, map[string]string{"id": id, "email": email})
}
func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	account, ok := s.authorize(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case "GET":
		s.queryJSON(w, r, "projects", "select id,name,created_at from projects where account_id=? order by created_at", account)
	case "POST":
		var req struct {
			Name string `json:"name"`
		}
		if !s.decode(w, r, &req) {
			return
		}
		if !validName(req.Name) {
			writeError(w, 400, "name must be 1–80 characters")
			return
		}
		id := uuid.NewString()
		_, e := s.db.ExecContext(r.Context(), "insert into projects values(?,?,?,?)", id, account, strings.TrimSpace(req.Name), time.Now().UnixMilli())
		if e != nil {
			writeError(w, 500, "could not create project")
			return
		}
		writeJSON(w, 201, map[string]string{"id": id, "name": req.Name})
	default:
		writeError(w, 405, "method not allowed")
	}
}
func validName(v string) bool { return len(strings.TrimSpace(v)) > 0 && len(v) <= 80 }
func (s *Server) queryJSON(w http.ResponseWriter, r *http.Request, key, query string, args ...any) {
	rows, e := s.db.QueryContext(r.Context(), query, args...)
	if e != nil {
		writeError(w, 500, "query failed")
		return
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		writeError(w, 500, "query failed")
		return
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		if e := rows.Scan(ptr...); e != nil {
			writeError(w, 500, "query failed")
			return
		}
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	if rows.Err() != nil {
		writeError(w, 500, "query failed")
		return
	}
	writeJSON(w, 200, map[string]any{key: out})
}
func (s *Server) projectResource(w http.ResponseWriter, r *http.Request) {
	account, ok := s.authorize(w, r)
	if !ok {
		return
	}
	project := r.PathValue("project")
	var owner string
	if e := s.db.QueryRowContext(r.Context(), "select account_id from projects where id=?", project).Scan(&owner); e != nil || owner != account {
		writeError(w, 404, "project not found")
		return
	}
	resource, id := r.PathValue("resource"), r.PathValue("id")
	switch resource {
	case "events":
		if r.Method != "POST" || id != "" {
			writeError(w, 405, "method not allowed")
			return
		}
		s.submitEvent(w, r, project)
	case "endpoints":
		if r.Method == "GET" {
			s.queryJSON(w, r, "endpoints", "select id,name,url,enabled,created_at from endpoints where project_id=? order by created_at desc", project)
			return
		}
		if r.Method == "DELETE" && id != "" {
			s.mutate(w, r, "update endpoints set enabled=0 where id=? and project_id=?", id, project)
			return
		}
		if r.Method != "POST" || id != "" {
			writeError(w, 405, "method not allowed")
			return
		}
		var req struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if !s.decode(w, r, &req) {
			return
		}
		if !validName(req.Name) {
			writeError(w, 400, "name must be 1–80 characters")
			return
		}
		if _, e := s.guard.Validate(r.Context(), req.URL); e != nil {
			writeError(w, 400, "destination URL is not allowed")
			return
		}
		id = uuid.NewString()
		secret := token()
		_, e := s.db.ExecContext(r.Context(), "insert into endpoints values(?,?,?,?,?,1,?)", id, project, req.Name, req.URL, secret, time.Now().UnixMilli())
		if e != nil {
			writeError(w, 500, "endpoint creation failed")
			return
		}
		writeJSON(w, 201, map[string]string{"id": id, "signing_secret": secret})
	case "keys":
		if r.Method == "GET" {
			s.queryJSON(w, r, "keys", "select id,name,prefix,revoked,created_at from api_keys where project_id=? order by created_at desc", project)
			return
		}
		if r.Method == "DELETE" && id != "" {
			s.mutate(w, r, "update api_keys set revoked=1 where id=? and project_id=?", id, project)
			return
		}
		if r.Method != "POST" || id != "" {
			writeError(w, 405, "method not allowed")
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if !s.decode(w, r, &req) {
			return
		}
		if !validName(req.Name) {
			writeError(w, 400, "name must be 1–80 characters")
			return
		}
		raw := "rl_live_" + token()
		id = uuid.NewString()
		_, e := s.db.ExecContext(r.Context(), "insert into api_keys values(?,?,?,?,?,0,?)", id, project, req.Name, digest(raw), raw[:16], time.Now().UnixMilli())
		if e != nil {
			writeError(w, 500, "key creation failed")
			return
		}
		writeJSON(w, 201, map[string]string{"id": id, "token": raw})
	case "usage":
		if r.Method != "GET" {
			writeError(w, 405, "method not allowed")
			return
		}
		var used int
		e := s.db.QueryRowContext(r.Context(), "select events from usage_months where project_id=? and month=?", project, time.Now().UTC().Format("2006-01")).Scan(&used)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			writeError(w, 500, "usage unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"events": used, "limit": monthlyLimit, "plan": "Free", "month": time.Now().UTC().Format("2006-01")})
	case "deliveries":
		if id == "" {
			if r.Method != "GET" {
				writeError(w, 405, "method not allowed")
				return
			}
			status := r.URL.Query().Get("status")
			s.queryJSON(w, r, "deliveries", `select d.delivery_id,d.status,d.attempts,d.target_url,d.last_error,d.created_at,e.event_type from delivery_jobs d join events e on e.id=d.event_id join event_scopes sc on sc.event_id=e.id where sc.project_id=? and (?='' or d.status=?) order by d.created_at desc limit 100`, project, status, status)
			return
		}
		var owned int
		if e := s.db.QueryRowContext(r.Context(), "select count(*) from delivery_jobs d join event_scopes sc on sc.event_id=d.event_id where d.delivery_id=? and sc.project_id=?", id, project).Scan(&owned); e != nil || owned != 1 {
			writeError(w, 404, "delivery not found")
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/replay") {
			s.handleReplay(w, r)
			return
		}
		if r.Method != "GET" {
			writeError(w, 405, "method not allowed")
			return
		}
		d, e := storage.GetDelivery(r.Context(), s.db, id)
		if e != nil {
			s.writeStorageError(w, e)
			return
		}
		attempts, e := storage.GetAttempts(r.Context(), s.db, id)
		if e != nil {
			s.writeStorageError(w, e)
			return
		}
		if attempts == nil {
			attempts = []storage.AttemptRecord{}
		}
		writeJSON(w, 200, map[string]any{"delivery": d, "attempts": attempts})
	default:
		writeError(w, 404, "resource not found")
	}
}
func (s *Server) mutate(w http.ResponseWriter, r *http.Request, q string, args ...any) {
	res, e := s.db.ExecContext(r.Context(), q, args...)
	if e != nil {
		writeError(w, 500, "update failed")
		return
	}
	n, e := res.RowsAffected()
	if e != nil || n == 0 {
		writeError(w, 404, "resource not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeError(w, 401, "project API key required")
		return
	}
	raw := strings.TrimPrefix(auth, "Bearer ")
	var project string
	if raw == "" {
		writeError(w, 401, "project API key required")
		return
	}
	if e := s.db.QueryRowContext(r.Context(), "select project_id from api_keys where token_hash=? and revoked=0", digest(raw)).Scan(&project); e != nil {
		writeError(w, 401, "invalid or revoked API key")
		return
	}
	s.submitEvent(w, r, project)
}
func (s *Server) submitEvent(w http.ResponseWriter, r *http.Request, project string) {
	var req struct {
		Event      string          `json:"event"`
		EndpointID string          `json:"endpoint_id"`
		Payload    json.RawMessage `json:"payload"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	var target, secret string
	if e := s.db.QueryRowContext(r.Context(), "select url,signing_secret from endpoints where id=? and project_id=? and enabled=1", req.EndpointID, project).Scan(&target, &secret); e != nil {
		writeError(w, 404, "enabled endpoint not found")
		return
	}
	if e := s.validateEvent(r.Context(), eventRequest{Event: req.Event, TargetURL: target, Payload: req.Payload}); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		writeError(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var count int
	e = tx.QueryRowContext(r.Context(), `insert into usage_months values(?,?,1) on conflict(project_id,month) do update set events=events+1 where events<? returning events`, project, time.Now().UTC().Format("2006-01"), monthlyLimit).Scan(&count)
	if errors.Is(e, sql.ErrNoRows) {
		writeError(w, 429, "monthly event limit reached")
		return
	}
	if e != nil {
		writeError(w, 503, "usage unavailable")
		return
	}
	eventID, deliveryID := uuid.NewString(), uuid.NewString()
	now := time.Now().UnixMilli()
	if _, e = tx.ExecContext(r.Context(), "insert into events values(?,?,?,?)", eventID, strings.TrimSpace(req.Event), string(req.Payload), now); e == nil {
		_, e = tx.ExecContext(r.Context(), "insert into event_scopes values(?,?,?,?)", eventID, project, req.EndpointID, secret)
	}
	if e == nil {
		_, e = tx.ExecContext(r.Context(), `insert into delivery_jobs(delivery_id,event_id,status,target_url,attempts,next_attempt_at,created_at,updated_at) values(?,?,'pending',?,0,?,?,?)`, deliveryID, eventID, target, now, now, now)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		writeError(w, 503, "event persistence failed")
		return
	}
	writeJSON(w, 202, eventAcceptedResponse{EventID: eventID, DeliveryID: deliveryID, Status: "accepted"})
}
