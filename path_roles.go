package gitea

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

const roleStoragePrefix = "role/"

// defaultTokenNamePrefix prefixes the Gitea token name of every minted token, so
// leaked/orphaned tokens are recognisable in Gitea's UI.
const defaultTokenNamePrefix = "vault"

// giteaRole binds a Gitea user and a scope set to a TTL policy. Each read of
// creds/<name> mints a unique, lease-bound token for Username with Scopes.
type giteaRole struct {
	// Username is the Gitea user the minted tokens belong to (a bot account).
	Username string `json:"username"`
	// Scopes are the Gitea access-token scopes granted to minted tokens.
	Scopes []string `json:"scopes"`
	// TokenNamePrefix prefixes each minted token's Gitea name.
	TokenNamePrefix string        `json:"token_name_prefix"`
	TTL             time.Duration `json:"ttl"`
	MaxTTL          time.Duration `json:"max_ttl"`
}

func (r *giteaRole) tokenNamePrefix() string {
	if r.TokenNamePrefix != "" {
		return r.TokenNamePrefix
	}
	return defaultTokenNamePrefix
}

func pathRole(b *giteaBackend) *framework.Path {
	return &framework.Path{
		Pattern: "roles/" + framework.GenericNameRegex("name"),
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "gitea",
			OperationSuffix: "role",
		},
		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeLowerCaseString,
				Description: "Name of the role.",
				Required:    true,
			},
			"username": {
				Type:        framework.TypeString,
				Description: "Gitea username that minted tokens belong to. The seeded admin mints tokens for this user.",
				Required:    true,
			},
			"scopes": {
				Type:        framework.TypeCommaStringSlice,
				Description: "Gitea access-token scopes granted to minted tokens (e.g. read:repository,write:issue). Validated against Gitea's scope set.",
				Required:    true,
			},
			"token_name_prefix": {
				Type:        framework.TypeString,
				Description: "Prefix for the Gitea token name of each minted token (default \"vault\").",
				Default:     defaultTokenNamePrefix,
			},
			"ttl": {
				Type:        framework.TypeDurationSecond,
				Description: "Default lease TTL for tokens minted from this role.",
			},
			"max_ttl": {
				Type:        framework.TypeDurationSecond,
				Description: "Maximum lease TTL for tokens minted from this role.",
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation:   &framework.PathOperation{Callback: b.pathRoleRead},
			logical.CreateOperation: &framework.PathOperation{Callback: b.pathRoleWrite},
			logical.UpdateOperation: &framework.PathOperation{Callback: b.pathRoleWrite},
			logical.DeleteOperation: &framework.PathOperation{Callback: b.pathRoleDelete},
		},
		ExistenceCheck:  b.pathRoleExistenceCheck,
		HelpSynopsis:    "Manage roles that mint short-lived Gitea tokens.",
		HelpDescription: "Each read of creds/<name> mints a unique, lease-bound Gitea access token for the role's user with the role's scopes.",
	}
}

func pathRolesList(b *giteaBackend) *framework.Path {
	return &framework.Path{
		Pattern: "roles/?$",
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "gitea",
			OperationSuffix: "roles",
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ListOperation: &framework.PathOperation{Callback: b.pathRolesList},
		},
		HelpSynopsis:    "List roles.",
		HelpDescription: "List the token-minting roles configured on this backend.",
	}
}

func (b *giteaBackend) pathRoleExistenceCheck(ctx context.Context, req *logical.Request, data *framework.FieldData) (bool, error) {
	role, err := b.getRole(ctx, req.Storage, data.Get("name").(string))
	if err != nil {
		return false, err
	}
	return role != nil, nil
}

func (b *giteaBackend) pathRoleRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	role, err := b.getRole(ctx, req.Storage, data.Get("name").(string))
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, nil
	}
	return &logical.Response{
		Data: map[string]interface{}{
			"username":          role.Username,
			"scopes":            role.Scopes,
			"token_name_prefix": role.tokenNamePrefix(),
			"ttl":               int64(role.TTL.Seconds()),
			"max_ttl":           int64(role.MaxTTL.Seconds()),
		},
	}, nil
}

func (b *giteaBackend) pathRoleWrite(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	name := data.Get("name").(string)
	role, err := b.getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	isCreate := role == nil
	if isCreate {
		role = &giteaRole{}
	}

	if v, ok := data.GetOk("username"); ok {
		role.Username = v.(string)
	}
	if v, ok := data.GetOk("scopes"); ok {
		scopes, serr := normalizeScopes(v.([]string))
		if serr != nil {
			return logical.ErrorResponse(serr.Error()), nil
		}
		role.Scopes = scopes
	}
	if v, ok := data.GetOk("token_name_prefix"); ok {
		role.TokenNamePrefix = v.(string)
	}
	if v, ok := data.GetOk("ttl"); ok {
		role.TTL = time.Duration(v.(int)) * time.Second
	}
	if v, ok := data.GetOk("max_ttl"); ok {
		role.MaxTTL = time.Duration(v.(int)) * time.Second
	}

	if role.Username == "" {
		return logical.ErrorResponse("username is required"), nil
	}
	if len(role.Scopes) == 0 {
		return logical.ErrorResponse("at least one scope is required"), nil
	}
	if role.MaxTTL > 0 && role.TTL > role.MaxTTL {
		return logical.ErrorResponse("ttl must not exceed max_ttl"), nil
	}

	return nil, setJSON(ctx, req.Storage, roleStoragePrefix+name, role)
}

func (b *giteaBackend) pathRoleDelete(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	return nil, req.Storage.Delete(ctx, roleStoragePrefix+data.Get("name").(string))
}

func (b *giteaBackend) pathRolesList(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	entries, err := req.Storage.List(ctx, roleStoragePrefix)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(entries), nil
}

func (b *giteaBackend) getRole(ctx context.Context, s logical.Storage, name string) (*giteaRole, error) {
	if name == "" {
		return nil, errors.New("missing role name")
	}
	entry, err := s.Get(ctx, roleStoragePrefix+name)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	role := &giteaRole{}
	if err := entry.DecodeJSON(role); err != nil {
		return nil, err
	}
	return role, nil
}
