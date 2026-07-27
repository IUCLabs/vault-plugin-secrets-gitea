package gitea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/vault/sdk/logical"
)

// fakeGitea is an in-memory stand-in for the Gitea REST API covering just the
// endpoints the plugin uses: token create/delete, admin password change, and
// whoami. Basic Auth is validated against the *current* admin credentials, so
// tests exercise password rotation exactly as production does.
type fakeGitea struct {
	mu       sync.Mutex
	adminU   string
	adminP   string
	nextID   int64
	tokens   map[string]storedToken // key: username/id
	minted   int
	adminHit int
}

type storedToken struct {
	id       int64
	name     string
	sha1     string
	scopes   []string
	username string
}

func newFakeGitea(adminUser, adminPass string) *fakeGitea {
	return &fakeGitea{
		adminU: adminUser,
		adminP: adminPass,
		tokens: map[string]storedToken{},
	}
}

func key(username, id string) string { return username + "/" + id }

func (f *fakeGitea) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		f.mu.Lock()
		validAdmin := ok && u == f.adminU && p == f.adminP
		f.mu.Unlock()
		if !validAdmin {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
			writeJSON(w, http.StatusOK, map[string]interface{}{"login": u, "is_admin": true})

		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/users/") && strings.HasSuffix(r.URL.Path, "/tokens"):
			username := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/users/"), "/tokens")
			var in createTokenOption
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			f.nextID++
			id := f.nextID
			f.minted++
			sha := "sha1-" + strconv.FormatInt(id, 10)
			f.tokens[key(username, strconv.FormatInt(id, 10))] = storedToken{
				id: id, name: in.Name, sha1: sha, scopes: in.Scopes, username: username,
			}
			f.mu.Unlock()
			writeJSON(w, http.StatusCreated, map[string]interface{}{
				"id": id, "name": in.Name, "sha1": sha, "token_last_eight": "lasteig8", "scopes": in.Scopes,
			})

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/tokens/"):
			// /api/v1/users/{username}/tokens/{id}
			rest := strings.TrimPrefix(r.URL.Path, "/api/v1/users/")
			parts := strings.SplitN(rest, "/tokens/", 2)
			if len(parts) != 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.mu.Lock()
			k := key(parts[0], parts[1])
			_, exists := f.tokens[k]
			if exists {
				delete(f.tokens, k)
			}
			f.mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
			var in editUserOption
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			f.adminHit++
			if in.Password != "" {
				f.adminP = in.Password
			}
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func (f *fakeGitea) has(username, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.tokens[key(username, id)]
	return ok
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newTestBackend(t *testing.T) (*giteaBackend, logical.Storage) {
	t.Helper()
	config := logical.TestBackendConfig()
	config.StorageView = &logical.InmemStorage{}
	b, err := Factory(context.Background(), config)
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	return b.(*giteaBackend), config.StorageView
}

func req(t *testing.T, b *giteaBackend, s logical.Storage, op logical.Operation, path string, data map[string]interface{}) *logical.Response {
	t.Helper()
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: op,
		Path:      path,
		Data:      data,
		Storage:   s,
	})
	if err != nil {
		t.Fatalf("%s %s: %v", op, path, err)
	}
	if resp != nil && resp.IsError() {
		t.Fatalf("%s %s: %v", op, path, resp.Error())
	}
	return resp
}

func configure(t *testing.T, b *giteaBackend, s logical.Storage, url string) {
	t.Helper()
	req(t, b, s, logical.CreateOperation, "config", map[string]interface{}{
		"gitea_url":      url,
		"admin_username": "bot-admin",
		"admin_password": "seed-password",
	})
}

func TestLifecycle(t *testing.T) {
	fake := newFakeGitea("bot-admin", "seed-password")
	srv := fake.server(t)
	defer srv.Close()

	b, s := newTestBackend(t)
	configure(t, b, s, srv.URL)

	req(t, b, s, logical.CreateOperation, "roles/teabot", map[string]interface{}{
		"username": "teabot",
		"scopes":   "read:repository,write:issue",
		"ttl":      "1h",
		"max_ttl":  "24h",
	})

	creds := req(t, b, s, logical.ReadOperation, "creds/teabot", nil)
	if creds.Secret == nil {
		t.Fatal("creds returned no secret")
	}
	tokenID, _ := creds.Data["token_id"].(string)
	tokenVal, _ := creds.Data["token"].(string)
	username, _ := creds.Data["username"].(string)
	if tokenID == "" || tokenVal == "" {
		t.Fatalf("creds missing token/token_id: %#v", creds.Data)
	}
	if username != "teabot" {
		t.Errorf("username = %q, want teabot", username)
	}
	if creds.Data["gitea_url"].(string) != srv.URL {
		t.Errorf("gitea_url = %q, want %q", creds.Data["gitea_url"], srv.URL)
	}
	if !fake.has("teabot", tokenID) {
		t.Error("minted token not present in gitea")
	}

	// Renew keeps the same secret.
	if _, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.RenewOperation,
		Secret:    creds.Secret,
		Storage:   s,
	}); err != nil {
		t.Fatalf("renew: %v", err)
	}

	// Revoke deletes the token from gitea.
	if _, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.RevokeOperation,
		Secret:    creds.Secret,
		Storage:   s,
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if fake.has("teabot", tokenID) {
		t.Error("token still present after revoke")
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	fake := newFakeGitea("bot-admin", "seed-password")
	srv := fake.server(t)
	defer srv.Close()

	b, s := newTestBackend(t)
	configure(t, b, s, srv.URL)
	req(t, b, s, logical.CreateOperation, "roles/teabot", map[string]interface{}{
		"username": "teabot", "scopes": "read:repository",
	})
	creds := req(t, b, s, logical.ReadOperation, "creds/teabot", nil)

	revoke := func() error {
		_, err := b.HandleRequest(context.Background(), &logical.Request{
			Operation: logical.RevokeOperation,
			Secret:    creds.Secret,
			Storage:   s,
		})
		return err
	}
	if err := revoke(); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	// A second revoke (token already gone → 404) must still succeed.
	if err := revoke(); err != nil {
		t.Fatalf("second revoke should be idempotent, got: %v", err)
	}
}

func TestScopeValidation(t *testing.T) {
	fake := newFakeGitea("bot-admin", "seed-password")
	srv := fake.server(t)
	defer srv.Close()

	b, s := newTestBackend(t)
	configure(t, b, s, srv.URL)

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.CreateOperation,
		Path:      "roles/bad",
		Data:      map[string]interface{}{"username": "teabot", "scopes": "read:repository,bogus:scope"},
		Storage:   s,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatal("expected error for invalid scope")
	}

	// Duplicate + mixed-case scopes normalise down cleanly.
	req(t, b, s, logical.CreateOperation, "roles/ok", map[string]interface{}{
		"username": "teabot", "scopes": "Read:Repository, read:repository ,write:issue",
	})
	role := req(t, b, s, logical.ReadOperation, "roles/ok", nil)
	got, _ := role.Data["scopes"].([]string)
	if len(got) != 2 || got[0] != "read:repository" || got[1] != "write:issue" {
		t.Errorf("normalised scopes = %v, want [read:repository write:issue]", got)
	}
}

func TestRoleRequiresUsernameAndScopes(t *testing.T) {
	fake := newFakeGitea("bot-admin", "seed-password")
	srv := fake.server(t)
	defer srv.Close()

	b, s := newTestBackend(t)
	configure(t, b, s, srv.URL)

	// Missing scopes.
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.CreateOperation,
		Path:      "roles/nos",
		Data:      map[string]interface{}{"username": "teabot"},
		Storage:   s,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatal("expected error for role without scopes")
	}
}

func TestConfigRequiresAdminAndVerifies(t *testing.T) {
	fake := newFakeGitea("bot-admin", "seed-password")
	srv := fake.server(t)
	defer srv.Close()

	b, s := newTestBackend(t)

	// Missing admin_password.
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.CreateOperation,
		Path:      "config",
		Data:      map[string]interface{}{"gitea_url": srv.URL, "admin_username": "bot-admin"},
		Storage:   s,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatal("expected error when admin_password missing")
	}

	// Wrong password fails verification against the fake (401).
	resp, err = b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.CreateOperation,
		Path:      "config",
		Data:      map[string]interface{}{"gitea_url": srv.URL, "admin_username": "bot-admin", "admin_password": "wrong"},
		Storage:   s,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatal("expected error when admin credentials fail verification")
	}

	// config read must never leak the password.
	configure(t, b, s, srv.URL)
	read := req(t, b, s, logical.ReadOperation, "config", nil)
	if _, leaked := read.Data["admin_password"]; leaked {
		t.Fatal("config read leaked admin_password")
	}
}

func TestCredsBeforeConfig(t *testing.T) {
	b, s := newTestBackend(t)
	// Roles can be written without config, but minting from one before config is
	// written must fail with the not-configured error.
	req(t, b, s, logical.CreateOperation, "roles/x", map[string]interface{}{
		"username": "teabot", "scopes": "read:repository",
	})
	_, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ReadOperation,
		Path:      "creds/x",
		Storage:   s,
	})
	if err == nil {
		t.Fatal("expected creds read to fail without config")
	}
}

func TestRotateRoot(t *testing.T) {
	fake := newFakeGitea("bot-admin", "seed-password")
	srv := fake.server(t)
	defer srv.Close()

	b, s := newTestBackend(t)
	configure(t, b, s, srv.URL)

	// Rotate the root password.
	rot := req(t, b, s, logical.UpdateOperation, "config/rotate-root", nil)
	if ok, _ := rot.Data["rotated"].(bool); !ok {
		t.Fatal("rotate-root did not report success")
	}

	// The fake now only accepts the new password; the engine's stored config must
	// have been updated to match, so minting still works after rotation.
	req(t, b, s, logical.CreateOperation, "roles/teabot", map[string]interface{}{
		"username": "teabot", "scopes": "read:repository",
	})
	creds := req(t, b, s, logical.ReadOperation, "creds/teabot", nil)
	if creds.Secret == nil {
		t.Fatal("mint after rotate-root failed")
	}

	// The stored password must no longer be the seed.
	cfg, err := getConfig(context.Background(), s)
	if err != nil {
		t.Fatalf("getConfig: %v", err)
	}
	if cfg.AdminPassword == "seed-password" {
		t.Error("admin_password was not changed by rotate-root")
	}
	if fake.adminHit == 0 {
		t.Error("rotate-root did not call the gitea admin API")
	}
}

func TestRotateRootRollback(t *testing.T) {
	fake := newFakeGitea("bot-admin", "seed-password")
	srv := fake.server(t)
	defer srv.Close()

	b, s := newTestBackend(t)
	configure(t, b, s, srv.URL)

	// Force the storage write to fail so rotate-root must roll back.
	failing := &failingStorage{Storage: s, failPut: true}
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "config/rotate-root",
		Storage:   failing,
	})
	if err == nil && (resp == nil || !resp.IsError()) {
		t.Fatal("expected rotate-root to fail when storage write fails")
	}

	// After rollback the seed password must work again: a normal config write
	// (which verifies against gitea) should succeed with the original password.
	configure(t, b, s, srv.URL)
}

// failingStorage wraps a storage and can be told to fail Put, to exercise the
// rotate-root rollback path.
type failingStorage struct {
	logical.Storage
	failPut bool
}

func (f *failingStorage) Put(ctx context.Context, entry *logical.StorageEntry) error {
	if f.failPut && entry.Key == configStoragePath {
		return errForcedPutFailure
	}
	return f.Storage.Put(ctx, entry)
}

var errForcedPutFailure = &forcedError{"forced put failure"}

type forcedError struct{ msg string }

func (e *forcedError) Error() string { return e.msg }
