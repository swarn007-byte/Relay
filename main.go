package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/swarn007-byte/Relay/internal/storage"
	"github.com/swarn007-byte/Relay/internal/worker"
)

type EventRequest struct {
	Event     string          `json:"event"`
	TargetURL string          `json:"target_url"`
	Payload   json.RawMessage `json:"payload"`
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Relay is running")
}

func eventValidator(event EventRequest) bool {
	if strings.TrimSpace(event.Event) == "" {
		return false
	}

	if strings.TrimSpace(event.TargetURL) == "" {
		return false
	}

	parsedURL, err := url.Parse(event.TargetURL)
	if err != nil {
		return false
	}

	if (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") ||
		parsedURL.Hostname() == "" {
		return false
	}

	if len(event.Payload) == 0 {
		return false
	}

	if bytes.Equal(bytes.TrimSpace(event.Payload), []byte("null")) {
		return false
	}

	return true
}
func eventHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var event EventRequest

	err := json.NewDecoder(r.Body).Decode(&event)
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if !eventValidator(event) {
		http.Error(w, "Invalid event data", http.StatusBadRequest)
		return
	}

	eventID := uuid.NewString()

	err = storage.SaveEvent(
		r.Context(),
		db,
		eventID,
		event.Event,
		event.TargetURL,
		event.Payload,
	)
	if err != nil {
		log.Println("Failed to save event:", err)
		http.Error(w, "Failed to save event", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "Event accepted:", eventID)
}

func main() {
	// Initialize database
	db, err := storage.InitDB()
	if err != nil {
		log.Fatal("Database initialization failed: ", err)
	}
	defer db.Close()

	log.Println("SQLite database connected successfully")

	if error := storage.CreateTables(db); error != nil {
		log.Fatal("Failed to create tables: ", error)
	}
	log.Println("Database tables initialized successfully")

	jobs := make(chan storage.DeliveryJob, 10)
	secret := []byte(os.Getenv("RELAY_WEBHOOK_SECRET"))

	if len(secret) < 32 {
		log.Fatal("RELAY_WEBHOOK_SECRET must contain at least 32 bytes")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	wg := worker.StartPool(ctx, db, jobs, 3, secret)

	if err := worker.ScheduleJobs(ctx, db, jobs, 10); err != nil {
		log.Println("Scheduling error:", err)
	}

	close(jobs)

	wg.Wait()

	log.Println("Worker pool test completed")

	// Initialize router
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", healthHandler)
	// mux.HandleFunc("POST /events", eventHandler)
	mux.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) {
		eventHandler(w, r, db)
	}) //w aur r automatically go's HTTP server hamare registered handler ko provide karta hai, but we have to pass db explicitly that's why we used closure here.

	// Start server
	log.Println("Relay server running on port 8081")

	if err := http.ListenAndServe(":8081", mux); err != nil {
		log.Fatal(err)
	}
}
