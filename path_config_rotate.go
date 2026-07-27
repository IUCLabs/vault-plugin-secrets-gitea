package gitea

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

// rotatedPasswordBytes is the entropy of a generated admin password (base64 of
// this many bytes, well within Gitea's 255-char and complexity limits).
const rotatedPasswordBytes = 32

func pathConfigRotateRoot(b *giteaBackend) *framework.Path {
	return &framework.Path{
		Pattern: "config/rotate-root",
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "gitea",
			OperationSuffix: "rotate-root",
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{Callback: b.pathConfigRotateRoot},
		},
		HelpSynopsis: "Rotate the seeded Gitea admin password.",
		HelpDescription: `
Generates a new random password for the seeded admin user, sets it via Gitea's
admin edit API using the current credentials, and stores it. After this the old
password no longer works and only Vault knows the new one.

Requirements and limits (documented honestly):
  - The admin must be a *local* Gitea user; external-auth users cannot have
    their password changed this way.
  - The admin account must not have TOTP/2FA enabled, because the engine
    authenticates with Basic Auth.
  - If persisting the new password fails, the engine attempts to roll the
    password back to the previous value. Should both the write and the rollback
    fail, the admin password must be reset manually and re-seeded via config.
`,
	}
}

func (b *giteaBackend) pathConfigRotateRoot(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	b.lock.Lock()
	defer b.lock.Unlock()

	config, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, errBackendNotConfigured
	}

	client, err := newClient(config)
	if err != nil {
		return nil, err
	}

	newPassword, err := generatePassword()
	if err != nil {
		return nil, fmt.Errorf("generating new password: %w", err)
	}

	// Change the password on Gitea first; nothing is stored until this succeeds,
	// so a failure here leaves the current credentials intact.
	if err := client.SetAdminPassword(ctx, config.AdminUsername, config.loginName(), config.AdminSourceID, newPassword); err != nil {
		return nil, fmt.Errorf("rotating admin password on gitea: %w", err)
	}

	oldPassword := config.AdminPassword
	config.AdminPassword = newPassword
	if err := setJSON(ctx, req.Storage, configStoragePath, config); err != nil {
		// The live password is now the new one but it is unstored — Vault would
		// be locked out. Roll Gitea back to the old password using the new one.
		rollbackClient := client.withPassword(newPassword)
		if rbErr := rollbackClient.SetAdminPassword(ctx, config.AdminUsername, config.loginName(), config.AdminSourceID, oldPassword); rbErr != nil {
			return nil, fmt.Errorf("CRITICAL: persisting rotated password failed (%v) and rollback failed (%v); reset the gitea admin password manually and re-seed config", err, rbErr)
		}
		return nil, fmt.Errorf("persisting rotated password failed, rolled back to previous password: %w", err)
	}

	return &logical.Response{
		Data: map[string]interface{}{
			"admin_username": config.AdminUsername,
			"rotated":        true,
		},
	}, nil
}

// generatePassword returns a URL-safe base64 password with rotatedPasswordBytes
// of entropy.
func generatePassword() (string, error) {
	buf := make([]byte, rotatedPasswordBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
