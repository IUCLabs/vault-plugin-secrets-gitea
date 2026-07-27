package gitea

import (
	"context"
	"errors"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

const configStoragePath = "config"

// giteaConfig is the connection to Gitea plus the seeded admin Basic-Auth
// credentials the engine uses to manage other users' tokens.
type giteaConfig struct {
	GiteaURL      string `json:"gitea_url"`
	AdminUsername string `json:"admin_username"`
	AdminPassword string `json:"admin_password"`
	// AdminLoginName / AdminSourceID are echoed into the admin edit call used by
	// rotate-root; Gitea requires login_name on EditUserOption. For a local user
	// login_name is the username and source_id is 0.
	AdminLoginName        string `json:"admin_login_name"`
	AdminSourceID         int64  `json:"admin_source_id"`
	CACert                string `json:"ca_cert"`
	TLSSkipVerify         bool   `json:"tls_skip_verify"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
}

// loginName resolves the login_name to send to Gitea's admin edit API, falling
// back to the admin username for local users.
func (c *giteaConfig) loginName() string {
	if c.AdminLoginName != "" {
		return c.AdminLoginName
	}
	return c.AdminUsername
}

func pathConfig(b *giteaBackend) *framework.Path {
	return &framework.Path{
		Pattern: "config",
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "gitea",
			OperationSuffix: "config",
		},
		Fields: map[string]*framework.FieldSchema{
			"gitea_url": {
				Type:        framework.TypeString,
				Description: "Base URL of the Gitea server, e.g. https://git.example.com.",
				Required:    true,
			},
			"admin_username": {
				Type:        framework.TypeString,
				Description: "Username of the Gitea site admin whose Basic-Auth credentials the engine uses to mint and delete tokens for other users.",
				Required:    true,
			},
			"admin_password": {
				Type:        framework.TypeString,
				Description: "Password of the Gitea site admin (Basic Auth). Write-only; rotate it in place with config/rotate-root.",
				DisplayAttrs: &framework.DisplayAttributes{
					Name:      "Admin Password",
					Sensitive: true,
				},
			},
			"admin_login_name": {
				Type:        framework.TypeString,
				Description: "login_name sent to Gitea's admin edit API during rotate-root (defaults to admin_username; use the external login name for non-local admins).",
			},
			"admin_source_id": {
				Type:        framework.TypeInt,
				Description: "Authentication source ID of the admin user, sent during rotate-root (0 for local users).",
				Default:     0,
			},
			"ca_cert": {
				Type:        framework.TypeString,
				Description: "PEM CA certificate that signed the Gitea server's TLS certificate.",
			},
			"tls_skip_verify": {
				Type:        framework.TypeBool,
				Description: "Skip TLS verification of the Gitea server (not recommended).",
				Default:     false,
			},
			"request_timeout_seconds": {
				Type:        framework.TypeInt,
				Description: "HTTP timeout in seconds for calls to Gitea (default 30).",
				Default:     30,
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation:   &framework.PathOperation{Callback: b.pathConfigRead},
			logical.CreateOperation: &framework.PathOperation{Callback: b.pathConfigWrite},
			logical.UpdateOperation: &framework.PathOperation{Callback: b.pathConfigWrite},
			logical.DeleteOperation: &framework.PathOperation{Callback: b.pathConfigDelete},
		},
		ExistenceCheck:  b.pathConfigExistenceCheck,
		HelpSynopsis:    "Configure the connection to Gitea and the seeded admin credentials.",
		HelpDescription: "Configure the Gitea URL, TLS settings, and the site-admin username/password the engine authenticates with. Roles then mint per-user tokens with these credentials.",
	}
}

func (b *giteaBackend) pathConfigExistenceCheck(ctx context.Context, req *logical.Request, _ *framework.FieldData) (bool, error) {
	config, err := getConfig(ctx, req.Storage)
	if err != nil {
		return false, err
	}
	return config != nil, nil
}

func (b *giteaBackend) pathConfigRead(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	config, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, nil
	}
	// admin_password is deliberately never returned.
	return &logical.Response{
		Data: map[string]interface{}{
			"gitea_url":               config.GiteaURL,
			"admin_username":          config.AdminUsername,
			"admin_login_name":        config.AdminLoginName,
			"admin_source_id":         config.AdminSourceID,
			"tls_skip_verify":         config.TLSSkipVerify,
			"request_timeout_seconds": config.RequestTimeoutSeconds,
		},
	}, nil
}

func (b *giteaBackend) pathConfigWrite(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	b.lock.Lock()
	defer b.lock.Unlock()

	config, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if config == nil {
		if req.Operation == logical.UpdateOperation {
			return nil, errors.New("config not found during update operation")
		}
		config = &giteaConfig{}
	}

	if v, ok := data.GetOk("gitea_url"); ok {
		config.GiteaURL = v.(string)
	}
	if v, ok := data.GetOk("admin_username"); ok {
		config.AdminUsername = v.(string)
	}
	if v, ok := data.GetOk("admin_password"); ok {
		config.AdminPassword = v.(string)
	}
	if v, ok := data.GetOk("admin_login_name"); ok {
		config.AdminLoginName = v.(string)
	}
	if v, ok := data.GetOk("admin_source_id"); ok {
		config.AdminSourceID = int64(v.(int))
	}
	if v, ok := data.GetOk("ca_cert"); ok {
		config.CACert = v.(string)
	}
	if v, ok := data.GetOk("tls_skip_verify"); ok {
		config.TLSSkipVerify = v.(bool)
	}
	if v, ok := data.GetOk("request_timeout_seconds"); ok {
		config.RequestTimeoutSeconds = v.(int)
	} else if req.Operation == logical.CreateOperation {
		config.RequestTimeoutSeconds = data.Get("request_timeout_seconds").(int)
	}

	if config.GiteaURL == "" {
		return logical.ErrorResponse("gitea_url is required"), nil
	}
	if config.AdminUsername == "" {
		return logical.ErrorResponse("admin_username is required"), nil
	}
	if config.AdminPassword == "" {
		return logical.ErrorResponse("admin_password is required"), nil
	}

	// Verify the seeded credentials authenticate and are a site admin before
	// storing them, so misconfiguration fails fast rather than at first mint.
	client, err := newClient(config)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := client.VerifyAdmin(ctx); err != nil {
		return logical.ErrorResponse("verifying gitea admin credentials: %s", err), nil
	}

	return nil, setJSON(ctx, req.Storage, configStoragePath, config)
}

func (b *giteaBackend) pathConfigDelete(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	return nil, req.Storage.Delete(ctx, configStoragePath)
}

func getConfig(ctx context.Context, s logical.Storage) (*giteaConfig, error) {
	entry, err := s.Get(ctx, configStoragePath)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	config := &giteaConfig{}
	if err := entry.DecodeJSON(config); err != nil {
		return nil, err
	}
	return config, nil
}

// setJSON stores a value as a JSON storage entry.
func setJSON(ctx context.Context, s logical.Storage, key string, value interface{}) error {
	entry, err := logical.StorageEntryJSON(key, value)
	if err != nil {
		return err
	}
	return s.Put(ctx, entry)
}
