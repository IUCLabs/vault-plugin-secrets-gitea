package gitea

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

const giteaTokenType = "gitea_token"

func (b *giteaBackend) giteaTokenSecret() *framework.Secret {
	return &framework.Secret{
		Type: giteaTokenType,
		Fields: map[string]*framework.FieldSchema{
			"token": {
				Type:        framework.TypeString,
				Description: "The Gitea access token value.",
			},
			"token_id": {
				Type:        framework.TypeString,
				Description: "The Gitea access token id (used for deletion).",
			},
			"token_name": {
				Type:        framework.TypeString,
				Description: "The Gitea access token name.",
			},
			"username": {
				Type:        framework.TypeString,
				Description: "The Gitea user the token belongs to.",
			},
		},
		Revoke: b.secretRevoke,
		Renew:  b.secretRenew,
	}
}

// secretRevoke deletes the minted Gitea token via the admin API. Returning an
// error lets Vault retry revocation; a token that is already gone (404) is
// treated as success inside DeleteToken so retries converge.
func (b *giteaBackend) secretRevoke(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	tokenID, err := internalString(req.Secret.InternalData, "token_id")
	if err != nil {
		return nil, err
	}
	username, err := internalString(req.Secret.InternalData, "username")
	if err != nil {
		return nil, err
	}

	b.lock.RLock()
	defer b.lock.RUnlock()

	client, err := b.clientFor(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if err := client.DeleteToken(ctx, username, tokenID); err != nil {
		return nil, fmt.Errorf("revoking gitea token %q for user %q: %w", tokenID, username, err)
	}
	return nil, nil
}

// secretRenew extends the Vault lease; the token material is unchanged. Gitea
// tokens never expire server-side, so the lease alone bounds the lifetime and a
// renew within max_ttl simply postpones deletion.
func (b *giteaBackend) secretRenew(_ context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	return &logical.Response{Secret: req.Secret}, nil
}

func internalString(data map[string]interface{}, key string) (string, error) {
	raw, ok := data[key]
	if !ok {
		return "", fmt.Errorf("secret is missing internal %s data", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", errors.New("secret internal " + key + " data is not a string")
	}
	return s, nil
}
