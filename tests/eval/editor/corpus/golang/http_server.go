package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
)

type user struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

var (
	mu    sync.RWMutex
	users = map[string]user{}
)

func listUsers(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]user, 0, len(users))
	for _, u := range users {
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

func createUser(w http.ResponseWriter, r *http.Request) {
	var u user
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if u.ID == "" || u.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and email required"})
		return
	}
	mu.Lock()
	users[u.ID] = u
	mu.Unlock()
	writeJSON(w, http.StatusCreated, u)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func main() {
	http.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listUsers(w, r)
		case http.MethodPost:
			createUser(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
