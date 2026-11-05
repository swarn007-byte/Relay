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
