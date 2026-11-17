# vault-plugin-secrets-gitea

A HashiCorp Vault and OpenBao secrets engine that mints short-lived, scoped
[Gitea](https://about.gitea.com/) access tokens. Each token is tied to a Vault
lease and deleted from Gitea when the lease is revoked or expires, so bots and
CI jobs never hold a standing token.

To manage the engine from Terraform, use
[terraform-provider-giteavaultsecret](https://github.com/IUCLabs/terraform-provider-giteavaultsecret).

## How it works

- The engine is configured once with a Gitea **site-admin** username and
  password. Gitea only allows token management over HTTP Basic Auth, and a site
  admin can manage tokens for any user
  ([go-gitea/gitea#21186](https://github.com/go-gitea/gitea/issues/21186)).
- A **role** names a Gitea user, the token scopes, and the lease TTLs.
- Reading `creds/<role>` creates a token for that user. Revoking the lease
  deletes it. Gitea tokens never expire on their own, so the lease is the only
  expiry.
- `config/rotate-root` replaces the admin password with a random one that only
  Vault knows.

## Requirements

- Vault or OpenBao. The end-to-end tests run against Vault 1.18 and the
  latest OpenBao release; other versions are untested.
- A Gitea version with scoped access tokens. The end-to-end tests use a mock
  Gitea API, so no specific Gitea release is tested.
- A local Gitea site-admin account without 2FA, for the engine to use
- Go 1.25 or newer to build from source

## Installation

Download a binary from the
[releases page](https://github.com/IUCLabs/vault-plugin-secrets-gitea/releases),
or build one:

```sh
git clone https://github.com/IUCLabs/vault-plugin-secrets-gitea.git
cd vault-plugin-secrets-gitea
make build   # writes dist/vault-plugin-secrets-gitea
```

Copy the binary into the server's `plugin_directory`, then register it and
enable the engine. With OpenBao, use `bao` in place of `vault`.

```sh
SHA256=$(sha256sum vault-plugin-secrets-gitea | cut -d' ' -f1)

vault plugin register -sha256="$SHA256" secret vault-plugin-secrets-gitea
vault secrets enable -path=gitea vault-plugin-secrets-gitea
```

After replacing the binary, register the new checksum and reload the plugin:

```sh
vault plugin register -sha256="$NEW_SHA256" secret vault-plugin-secrets-gitea
vault write sys/plugins/reload/backend plugin=vault-plugin-secrets-gitea
```

## Configuration

```sh
vault write gitea/config \
  gitea_url=https://gitea.example.com \
  admin_username=vault-admin \
  admin_password="$GITEA_ADMIN_PASSWORD"

# Replace the seeded password with one only Vault knows.
vault write -f gitea/config/rotate-root
```

The write fails unless the credentials belong to a Gitea site admin.

## Usage

Create a role:

```sh
vault write gitea/roles/ci-bot \
  username=ci-bot \
  scopes=read:repository,write:issue \
  ttl=1h \
  max_ttl=24h
```

Mint a token:

```sh
vault read gitea/creds/ci-bot
```

```text
Key                Value
---                -----
lease_id           gitea/creds/ci-bot/3Yb8...
lease_duration     1h
lease_renewable    true
gitea_url          https://gitea.example.com
scopes             [read:repository write:issue]
token              5f1c...
token_id           42
token_name         vault-ci-bot-1a2b3c4d
username           ci-bot
```

Revoke it, which deletes the token from Gitea:

```sh
vault lease revoke gitea/creds/ci-bot/3Yb8...
```

## Documentation

- [config and config/rotate-root](docs/config.md)
- [roles](docs/roles.md)
- [creds](docs/creds.md)

## Development

```sh
make build   # build the plugin into dist/
make test    # unit tests
make e2e     # full lifecycle against Vault and OpenBao with a mock Gitea (Docker)
```

## License

[MIT](LICENSE)
