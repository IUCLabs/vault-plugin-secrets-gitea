package gitea

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultHTTPTimeout = 30 * time.Second

// giteaClient talks to the Gitea REST API using the seeded site-admin
// credentials over HTTP Basic Auth. Basic Auth is mandatory: Gitea's
// token-management endpoints reject token/bearer auth (go-gitea/gitea#21186).
type giteaClient struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// accessToken mirrors the subset of Gitea's AccessToken JSON we read. The token
// value (sha1) is only ever returned in the create response.
type accessToken struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	SHA1           string   `json:"sha1"`
	TokenLastEight string   `json:"token_last_eight"`
	Scopes         []string `json:"scopes"`
}

// createTokenOption is Gitea's CreateAccessTokenOption request body.
type createTokenOption struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes,omitempty"`
}

// editUserOption is the subset of Gitea's EditUserOption used to rotate the
// admin password. Gitea binds login_name as Required, so it must be sent even
// for a password-only change.
type editUserOption struct {
	LoginName string `json:"login_name"`
	SourceID  int64  `json:"source_id"`
	Password  string `json:"password"`
}

func newClient(cfg *giteaConfig) (*giteaClient, error) {
	if cfg == nil {
		return nil, errors.New("gitea client configuration is nil")
	}
	if cfg.GiteaURL == "" {
		return nil, errors.New("gitea_url is required")
	}
	if cfg.AdminUsername == "" || cfg.AdminPassword == "" {
		return nil, errors.New("admin_username and admin_password are required")
	}

	timeout := defaultHTTPTimeout
	if cfg.RequestTimeoutSeconds > 0 {
		timeout = time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify} //nolint:gosec // opt-in via config
	if cfg.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, errors.New("ca_cert is not a valid PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}

	return &giteaClient{
		baseURL:  strings.TrimRight(cfg.GiteaURL, "/"),
		username: cfg.AdminUsername,
		password: cfg.AdminPassword,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}, nil
}

// withPassword returns a shallow copy of the client authenticating with a
// different password. Used to roll a rotation back to the previous password.
func (c *giteaClient) withPassword(password string) *giteaClient {
	clone := *c
	clone.password = password
	return &clone
}

// errNotFound flags a 404 so callers can treat absence as non-fatal.
var errNotFound = errors.New("not found")

// CreateToken mints a new access token for username with the given scopes and
// returns its value (sha1) and numeric id (as a string). The admin's Basic Auth
// credentials authorise minting for another user (reqSelfOrAdmin).
func (c *giteaClient) CreateToken(ctx context.Context, username, name string, scopes []string) (value, id string, err error) {
	body := createTokenOption{Name: name, Scopes: scopes}
	var out accessToken
	if err := c.do(ctx, http.MethodPost, "/api/v1/users/"+username+"/tokens", body, &out); err != nil {
		return "", "", err
	}
	if out.SHA1 == "" {
		return "", "", errors.New("gitea returned an empty token value")
	}
	return out.SHA1, strconv.FormatInt(out.ID, 10), nil
}

// DeleteToken removes an access token from username by its id (Gitea also
// accepts the token name here). A missing token is treated as success.
func (c *giteaClient) DeleteToken(ctx context.Context, username, id string) error {
	if id == "" {
		return nil
	}
	err := c.do(ctx, http.MethodDelete, "/api/v1/users/"+username+"/tokens/"+id, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

// SetAdminPassword changes the seeded admin user's password via the admin API.
// login_name / source_id are echoed from config because Gitea requires them.
func (c *giteaClient) SetAdminPassword(ctx context.Context, username, loginName string, sourceID int64, newPassword string) error {
	body := editUserOption{LoginName: loginName, SourceID: sourceID, Password: newPassword}
	return c.do(ctx, http.MethodPatch, "/api/v1/admin/users/"+username, body, nil)
}

// VerifyAdmin confirms the seeded credentials authenticate and belong to a site
// admin, returning a clear error otherwise. Used to fail config writes fast.
func (c *giteaClient) VerifyAdmin(ctx context.Context) error {
	var out struct {
		Login   string `json:"login"`
		IsAdmin bool   `json:"is_admin"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/user", nil, &out); err != nil {
		return err
	}
	if !out.IsAdmin {
		return fmt.Errorf("user %q is not a Gitea site admin; the engine requires an admin to manage other users' tokens", out.Login)
	}
	return nil
}

func (c *giteaClient) do(ctx context.Context, method, path string, payload, out interface{}) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling gitea %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gitea %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decoding gitea response: %w", err)
	}
	return nil
}
