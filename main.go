package main

import (
	"fmt"
	"log"
	"net/http"
	"encoding/json"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Relay is running")
}
type EventRequest struct {
	Event  string
	TargetURL string
	Payload json.RawMessage
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", healthHandler)

	fmt.Println("Relay server running on port 8080")

	err := http.ListenAndServe(":8080", mux)
	if err != nil {
		log.Fatal(err)
	}
}
func eventHandler(w http.ResponseWriter, r *http.Request) {
    var event EventRequest

    err := json.NewDecoder(r.Body).Decode(&event)
    if err != nil {
        http.Error(w, "Invalid JSON", http.StatusBadRequest)
        return
    }

    fmt.Println("Received event:", event.Event)
}
import (
    "bytes"
    "encoding/json"
    "net/url"
    "strings"
)

type EventRequest struct {
    Event     string          `json:"event"`
    TargetURL string          `json:"target_url"`
    Payload   json.RawMessage `json:"payload"`
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
