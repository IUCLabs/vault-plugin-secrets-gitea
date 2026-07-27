// Command mockgitea is an in-memory stand-in for the subset of the Gitea REST
// API that vault-plugin-secrets-gitea uses: whoami, per-user access token
// create/delete, and admin password change. It is used by the e2e tests — no
// real Gitea, database, or git required. Not for production use.
//
// Authentication is HTTP Basic Auth against the *current* admin credentials
// (MOCKGITEA_ADMIN_USER / MOCKGITEA_ADMIN_PASS); a successful admin password
// change updates the accepted credentials, exactly as the plugin's rotate-root
// relies on.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

type createTokenOption struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

type editUserOption struct {
	LoginName string `json:"login_name"`
	SourceID  int64  `json:"source_id"`
	Password  string `json:"password"`
}

type store struct {
	mu     sync.Mutex
	adminU string
	adminP string
	nextID int64
	tokens map[string]bool // key: username/id
}

func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (s *store) authOK(r *http.Request) bool {
	u, p, ok := r.BasicAuth()
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return u == s.adminU && p == s.adminP
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *store) handleUser(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	login := s.adminU
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"login": login, "is_admin": true})
}

func (s *store) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// /api/v1/users/{username}/tokens[/{id}]
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/users/")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(rest, "/tokens"):
		username := strings.TrimSuffix(rest, "/tokens")
		var in createTokenOption
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.nextID++
		id := s.nextID
		s.tokens[username+"/"+strconv.FormatInt(id, 10)] = true
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"id":               id,
			"name":             in.Name,
			"sha1":             randHex(20),
			"token_last_eight": randHex(4),
			"scopes":           in.Scopes,
		})
	case r.Method == http.MethodDelete && strings.Contains(rest, "/tokens/"):
		parts := strings.SplitN(rest, "/tokens/", 2)
		if len(parts) != 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		k := parts[0] + "/" + parts[1]
		s.mu.Lock()
		exists := s.tokens[k]
		delete(s.tokens, k)
		s.mu.Unlock()
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *store) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPatch {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var in editUserOption
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if in.Password != "" {
		s.adminP = in.Password
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func main() {
	addr := os.Getenv("MOCKGITEA_ADDR")
	if addr == "" {
		addr = ":3000"
	}
	adminU := os.Getenv("MOCKGITEA_ADMIN_USER")
	if adminU == "" {
		adminU = "bot-admin"
	}
	adminP := os.Getenv("MOCKGITEA_ADMIN_PASS")
	if adminP == "" {
		adminP = "seed-password"
	}
	s := &store{adminU: adminU, adminP: adminP, tokens: map[string]bool{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/user", s.handleUser)
	mux.HandleFunc("/api/v1/users/", s.handleUsers)
	mux.HandleFunc("/api/v1/admin/users/", s.handleAdminUsers)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	log.Printf("mock gitea API listening on %s (admin %s)", addr, adminU)
	log.Fatal(http.ListenAndServe(addr, mux)) //nolint:gosec // test-only mock
}
