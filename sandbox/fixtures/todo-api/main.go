// Command todo serves a tiny JSON API for a todo list.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

func main() {
	log.Fatal(http.ListenAndServe(":8080", newServer(NewStore())))
}

func newServer(s *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /todos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.List())
	})
	mux.HandleFunc("POST /todos", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, s.Add(in.Title))
	})
	mux.HandleFunc("POST /todos/{id}/done", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		t, err := s.Complete(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, t)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
