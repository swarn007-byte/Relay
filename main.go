package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/swarn007-byte/Relay/internals/storage"
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

func eventHandler(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("Received event:", event.Event)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "Event received successfully")
}

func main() {
	// Initialize database
	db, err := storage.InitDB()
	if err != nil {
		log.Fatal("Database initialization failed: ", err)
	}
	defer db.Close()

	log.Println("SQLite database connected successfully")

	// Initialize router
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("POST /events", eventHandler)

	// Start server
	log.Println("Relay server running on port 8080")

	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatal(err)
	}
}
