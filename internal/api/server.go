package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/swarn007-byte/Relay/internal/config"
	"github.com/swarn007-byte/Relay/internal/security"
	"github.com/swarn007-byte/Relay/internal/storage"
)

type Server struct {
	cfg   *config.Config
	db    *sql.DB
	guard *security.Guard
}

type eventRequest struct {
	Event     string          `json:"event"`
	TargetURL string          `json:"target_url"`
	Payload   json.RawMessage `json:"payload"`
}

type eventAcceptedResponse struct {
	EventID    string `json:"event_id"`
	DeliveryID string `json:"delivery_id"`
	Status     string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func New(cfg *config.Config, db *sql.DB, guard *security.Guard) *Server {
	return &Server{cfg: cfg, db: db, guard: guard}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	if s.cfg.SaaS {
		s.saasRoutes(mux)
	} else {
		mux.HandleFunc("POST /events", s.handleCreateEvent)
		mux.Handle("GET /deliveries", s.requireAdmin(http.HandlerFunc(s.handleListDeliveries)))
		mux.Handle("GET /deliveries/{id}", s.requireAdmin(http.HandlerFunc(s.handleGetDelivery)))
		mux.Handle("GET /deliveries/{id}/attempts", s.requireAdmin(http.HandlerFunc(s.handleGetAttempts)))
		mux.Handle("POST /deliveries/{id}/replay", s.requireAdmin(http.HandlerFunc(s.handleReplay)))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	var req eventRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must contain a single JSON value")
		return
	}

	if err := s.validateEvent(r.Context(), req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	eventID := uuid.NewString()
	deliveryID, err := storage.SaveEvent(
		r.Context(),
		s.db,
		eventID,
		strings.TrimSpace(req.Event),
		req.TargetURL,
		req.Payload,
	)
	if err != nil {
		log.Println("Failed to save event:", err)
		writeError(w, http.StatusInternalServerError, "failed to save event")
		return
	}

	writeJSON(w, http.StatusAccepted, eventAcceptedResponse{
		EventID:    eventID,
		DeliveryID: deliveryID,
		Status:     "accepted",
	})
}

func (s *Server) validateEvent(ctx context.Context, req eventRequest) error {
	name := strings.TrimSpace(req.Event)
	if name == "" {
		return errors.New("event is required")
	}
	if len(name) > 256 {
		return errors.New("event name must be at most 256 characters")
	}
	if strings.TrimSpace(req.TargetURL) == "" {
		return errors.New("target_url is required")
	}
	if _, err := s.guard.Validate(ctx, req.TargetURL); err != nil {
		return fmt.Errorf("target_url: %w", err)
	}
	if len(req.Payload) == 0 {
		return errors.New("payload is required")
	}
	if !json.Valid(req.Payload) {
		return errors.New("payload must be valid JSON")
	}
	if string(req.Payload) == "null" {
		return errors.New("payload must not be null")
	}
	return nil
}

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "pending" && status != "processing" &&
		status != "delivered" && status != "dead" {
		writeError(w, http.StatusBadRequest, "status must be one of pending, processing, delivered, dead")
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 200")
			return
		}
		limit = parsed
	}

	deliveries, err := storage.ListDeliveries(r.Context(), s.db, status, limit)
	if err != nil {
		log.Println("Failed to list deliveries:", err)
		writeError(w, http.StatusInternalServerError, "failed to list deliveries")
		return
	}
	if deliveries == nil {
		deliveries = []storage.DeliveryDetail{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deliveries": deliveries})
}

func (s *Server) handleGetDelivery(w http.ResponseWriter, r *http.Request) {
	id, ok := s.deliveryID(w, r)
	if !ok {
		return
	}

	delivery, err := storage.GetDelivery(r.Context(), s.db, id)
	if err != nil {
		s.writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, delivery)
}

func (s *Server) handleGetAttempts(w http.ResponseWriter, r *http.Request) {
	id, ok := s.deliveryID(w, r)
	if !ok {
		return
	}

	if _, err := storage.GetDelivery(r.Context(), s.db, id); err != nil {
		s.writeStorageError(w, err)
		return
	}

	attempts, err := storage.GetAttempts(r.Context(), s.db, id)
	if err != nil {
		log.Println("Failed to list attempts:", err)
		writeError(w, http.StatusInternalServerError, "failed to list attempts")
		return
	}
	if attempts == nil {
		attempts = []storage.AttemptRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"attempts": attempts})
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	id, ok := s.deliveryID(w, r)
	if !ok {
		return
	}

	newID, err := storage.ReplayDelivery(r.Context(), s.db, id)
	if err != nil {
		s.writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"delivery_id": newID,
		"status":      "pending",
		"replayed":    id,
	})
}

func (s *Server) deliveryID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "delivery id must be a UUID")
		return "", false
	}
	return id, true
}

func (s *Server) writeStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, storage.ErrNotReplayable):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, storage.ErrActiveDelivery):
		writeError(w, http.StatusConflict, err.Error())
	default:
		log.Println("Storage error:", err)
		writeError(w, http.StatusInternalServerError, "internal storage error")
	}
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			writeError(w, http.StatusForbidden,
				"management API is disabled; set RELAY_ADMIN_TOKEN to enable it")
			return
		}

		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if !constantTimeEqual(strings.TrimPrefix(auth, prefix), s.cfg.AdminToken) {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeEqual(a, b string) bool {
	hashA := sha256.Sum256([]byte(a))
	hashB := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(hashA[:], hashB[:]) == 1
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Println("Failed to encode response:", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
