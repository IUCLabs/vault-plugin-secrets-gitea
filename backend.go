// Package gitea implements a Vault / OpenBao secrets engine that mints
// ephemeral, scoped Gitea personal access tokens on demand.
//
// Static Gitea bot users (CI identities, automation accounts, ...) should never
// hold long-lived tokens. The engine is seeded with a single Gitea *site admin*
// credential (username + password, HTTP Basic Auth) and, on each read of
// creds/<role>, mints a fresh access token for the role's target user via the
// admin API (POST /api/v1/users/{username}/tokens). Each minted token is bound
// to a Vault lease and deleted from Gitea (DELETE .../tokens/{id}) when the
// lease expires or is revoked.
//
// Why Basic Auth and not an admin token: Gitea's token-management endpoints are
// guarded by reqBasicOrRevProxyAuth() — you cannot create or delete a token
// using token/bearer auth, only Basic Auth or reverse-proxy auth (see
// go-gitea/gitea#21186). The same routes are guarded by reqSelfOrAdmin(), so a
// site admin authenticating with Basic Auth may mint and delete tokens for *any*
// user by naming them in the path. That is exactly the mechanism this engine
// relies on, which is why the seeded credential is an admin username+password.
//
// Gitea access tokens have no server-side expiry: once created a token lives
// until it is deleted. The Vault lease is therefore the *only* expiry mechanism
// — lease revocation deletes the token, and that is what bounds its lifetime.
package gitea

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

// errBackendNotConfigured is returned when a credential is requested before the
// Gitea connection has been configured.
var errBackendNotConfigured = errors.New("gitea backend not configured; write config first")

type giteaBackend struct {
	*framework.Backend

	// lock serialises root-credential rotation against credential issuance so a
	// mint never races a password change out from under it.
	lock sync.RWMutex
}

// Factory returns a configured Gitea secrets backend.
func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
	b := backend()
	if err := b.Setup(ctx, conf); err != nil {
		return nil, err
	}
	return b, nil
}

func backend() *giteaBackend {
	b := &giteaBackend{}

	b.Backend = &framework.Backend{
		Help:        strings.TrimSpace(backendHelp),
		BackendType: logical.TypeLogical,
		PathsSpecial: &logical.Paths{
			SealWrapStorage: []string{configStoragePath},
		},
		Paths: framework.PathAppend(
			[]*framework.Path{
				pathConfig(b),
				pathConfigRotateRoot(b),
				pathRole(b),
				pathRolesList(b),
				pathCredentials(b),
			},
		),
		Secrets: []*framework.Secret{
			b.giteaTokenSecret(),
		},
	}

	return b
}

// clientFor builds a Gitea client from the stored config, authenticated with the
// seeded admin Basic-Auth credentials.
func (b *giteaBackend) clientFor(ctx context.Context, s logical.Storage) (*giteaClient, error) {
	config, err := getConfig(ctx, s)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, errBackendNotConfigured
	}
	return newClient(config)
}

const backendHelp = `
The gitea secrets engine mints ephemeral, scoped Gitea access tokens.

Seed the engine with a Gitea site-admin username and password (Basic Auth);
Gitea only permits token management via Basic Auth, and a site admin may manage
tokens for any user. Roles bind a target Gitea username to a set of token scopes
and TTLs; each read of creds/<role> mints a fresh token for that user, bound to
a Vault lease and deleted from Gitea on revocation. Gitea tokens never expire
server-side, so the Vault lease is the only thing that bounds their lifetime.

Use config/rotate-root to rotate the seeded admin password in place.
`
