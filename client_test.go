package gitea

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, url string) *giteaClient {
	t.Helper()
	c, err := newClient(&giteaConfig{
		GiteaURL:      url,
		AdminUsername: "bot-admin",
		AdminPassword: "seed-password",
	})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	return c
}

func TestClientCreateAndDeleteToken(t *testing.T) {
	fake := newFakeGitea("bot-admin", "seed-password")
	srv := fake.server(t)
	defer srv.Close()

	c := testClient(t, srv.URL)
	ctx := context.Background()

	value, id, err := c.CreateToken(ctx, "ci-bot", "vault-ci-bot-abcd1234", []string{"read:repository"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if value == "" || id == "" {
		t.Fatalf("CreateToken returned empty value/id: %q %q", value, id)
	}
	if !fake.has("ci-bot", id) {
		t.Fatal("token not stored in fake")
	}

	if err := c.DeleteToken(ctx, "ci-bot", id); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if fake.has("ci-bot", id) {
		t.Fatal("token still present after delete")
	}

	// Deleting a non-existent token (404) is treated as success.
	if err := c.DeleteToken(ctx, "ci-bot", "999999"); err != nil {
		t.Fatalf("DeleteToken of missing token should succeed, got: %v", err)
	}
}

func TestClientUnauthorized(t *testing.T) {
	fake := newFakeGitea("bot-admin", "correct-password")
	srv := fake.server(t)
	defer srv.Close()

	c := testClient(t, srv.URL) // uses "seed-password", which is wrong here
	_, _, err := c.CreateToken(context.Background(), "ci-bot", "x", []string{"read:repository"})
	if err == nil {
		t.Fatal("expected unauthorized error with wrong password")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
}

func TestClientVerifyAdminNonAdmin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"login": "bot-admin", "is_admin": false})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	err := c.VerifyAdmin(context.Background())
	if err == nil {
		t.Fatal("expected error for non-admin user")
	}
	if !strings.Contains(err.Error(), "not a Gitea site admin") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClientRequiresCredentials(t *testing.T) {
	if _, err := newClient(&giteaConfig{GiteaURL: "https://git.example.com", AdminUsername: "u"}); err == nil {
		t.Fatal("expected error when admin_password missing")
	}
	if _, err := newClient(&giteaConfig{AdminUsername: "u", AdminPassword: "p"}); err == nil {
		t.Fatal("expected error when gitea_url missing")
	}
	if _, err := newClient(&giteaConfig{GiteaURL: "x", AdminUsername: "u", AdminPassword: "p", CACert: "not-a-pem"}); err == nil {
		t.Fatal("expected error for invalid ca_cert")
	}
}
