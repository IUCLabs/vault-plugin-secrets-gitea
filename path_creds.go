package gitea

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

func pathCredentials(b *giteaBackend) *framework.Path {
	return &framework.Path{
		Pattern: "creds/" + framework.GenericNameRegex("name"),
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "gitea",
			OperationSuffix: "credentials",
		},
		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeLowerCaseString,
				Description: "Name of the role to mint a token for.",
				Required:    true,
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation:   &framework.PathOperation{Callback: b.pathCredentialsRead},
			logical.UpdateOperation: &framework.PathOperation{Callback: b.pathCredentialsRead},
		},
		HelpSynopsis:    "Mint a short-lived Gitea token from a role.",
		HelpDescription: "Reading this path mints a new, lease-bound Gitea access token for the role's user; the token is deleted from Gitea when the lease is revoked.",
	}
}

func (b *giteaBackend) pathCredentialsRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	roleName := data.Get("name").(string)

	// Read lock: allow concurrent mints, but block while root rotation holds the
	// write lock so a mint never uses a password being changed out from under it.
	b.lock.RLock()
	defer b.lock.RUnlock()

	role, err := b.getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return logical.ErrorResponse("role %q does not exist", roleName), nil
	}

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

	ttl, maxTTL := b.resolveTTLs(role.TTL, role.MaxTTL)

	suffix, err := uuid.GenerateUUID()
	if err != nil {
		return nil, fmt.Errorf("generating token name suffix: %w", err)
	}
	tokenName := fmt.Sprintf("%s-%s-%s", role.tokenNamePrefix(), roleName, suffix[:8])

	value, id, err := client.CreateToken(ctx, role.Username, tokenName, role.Scopes)
	if err != nil {
		return nil, fmt.Errorf("minting gitea token: %w", err)
	}

	internal := map[string]interface{}{
		"token_id":   id,
		"token_name": tokenName,
		"username":   role.Username,
	}
	external := map[string]interface{}{
		"token":      value,
		"token_id":   id,
		"token_name": tokenName,
		"username":   role.Username,
		"gitea_url":  config.GiteaURL,
		"scopes":     role.Scopes,
	}

	resp := b.Secret(giteaTokenType).Response(external, internal)
	resp.Secret.TTL = ttl
	resp.Secret.MaxTTL = maxTTL
	resp.Secret.Renewable = true
	return resp, nil
}

// resolveTTLs clamps a role's TTL/MaxTTL against the mount and system limits.
func (b *giteaBackend) resolveTTLs(roleTTL, roleMaxTTL time.Duration) (ttl, maxTTL time.Duration) {
	sysMaxTTL := b.System().MaxLeaseTTL()
	maxTTL = roleMaxTTL
	if maxTTL <= 0 || maxTTL > sysMaxTTL {
		maxTTL = sysMaxTTL
	}
	ttl = roleTTL
	if ttl <= 0 {
		ttl = b.System().DefaultLeaseTTL()
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	return ttl, maxTTL
}
