# vault-plugin-secrets-gitea

A Vault / OpenBao secrets engine that mints **ephemeral, scoped Gitea access
tokens** on demand.

## Why

Static Gitea bot users (teabot personalities, CI identities, ...) should never
hold long-lived personal access tokens: a leaked token is valid until someone
notices and deletes it, and Gitea tokens **never expire on their own**. This
engine removes standing tokens entirely:

- You seed it once with a single Gitea **site-admin** credential.
- A **role** binds a target Gitea username to a set of token scopes and TTLs.
- Each read of `creds/<role>` mints a fresh token for that user, bound to a
  Vault lease, and **deletes it from Gitea when the lease is revoked or expires**.

### Why an admin username + password, not an admin token

Gitea's token-management endpoints (`POST/DELETE /api/v1/users/{username}/tokens`)
are guarded by `reqBasicOrRevProxyAuth()` — they **reject token/bearer auth** and
accept only HTTP Basic Auth or reverse-proxy auth
([go-gitea/gitea#21186](https://github.com/go-gitea/gitea/issues/21186)). The
same routes are also guarded by `reqSelfOrAdmin()`, so a **site admin** using
Basic Auth may mint and delete tokens for *any* user by naming them in the path.
That is the mechanism this engine relies on, which is why the seeded credential
is an admin **username + password**, not a token.

### The Vault lease is the only expiry

Gitea access tokens have no server-side TTL. The Vault lease is therefore the
sole expiry mechanism: when the lease is revoked or reaches `max_ttl`, the engine
issues `DELETE /api/v1/users/{username}/tokens/{id}` to remove the token. A
delete that returns 404 (token already gone) is treated as success, so revocation
is idempotent and Vault's retries converge.

## Paths

| Path | Description |
|------|-------------|
| `config` | Gitea URL + TLS settings + seeded admin `admin_username`/`admin_password`. Verifies the credentials are a site admin on write. |
| `config/rotate-root` | Rotate the seeded admin password in place (generates a new random password, changes it via the admin API, stores it). |
| `roles/<name>` | Mint policy: `username`, `scopes`, `ttl`, `max_ttl`, `token_name_prefix`. |
| `roles` | List roles. |
| `creds/<role>` | Read to mint a short-lived, lease-bound token for the role's user. |

## Scopes

`scopes` is validated against Gitea's scope set (see
`models/auth/access_token_scope.go`): `all`, `public-only`, and the `read:` /
`write:` forms of `activitypub`, `admin`, `misc`, `notification`, `organization`,
`package`, `issue`, `repository`, `user`. `write:` implies `read:`.

## Usage

```sh
vault secrets enable -path=gitea vault-plugin-secrets-gitea

# Seed with a Gitea site-admin username + password (Basic Auth).
vault write gitea/config \
  gitea_url=https://git.example.com \
  admin_username=bot-admin \
  admin_password='...' \
  ca_cert=@gitea-ca.pem

# Immediately rotate the seeded password so only Vault knows it.
vault write -f gitea/config/rotate-root

# A role that mints 1h tokens for the "teabot" user, scoped to repo + issues.
vault write gitea/roles/teabot username=teabot \
  scopes=read:repository,write:issue ttl=1h max_ttl=24h

# Mint one. The token is deleted from Gitea when the lease is revoked.
vault read gitea/creds/teabot
```

### Root rotation requirements & limits

`config/rotate-root` changes the seeded admin's password via
`PATCH /api/v1/admin/users/{admin_username}`. Honestly documented constraints:

- The admin must be a **local** Gitea user (external-auth users can't have their
  password changed this way). Gitea requires `login_name` on the edit call; the
  engine sends `admin_login_name` (default: `admin_username`) and `admin_source_id`
  (default `0`, i.e. local).
- The admin account must **not** have TOTP/2FA enabled — Basic Auth with 2FA
  requires an OTP header the engine cannot supply.
- Rotation changes Gitea first, then persists to Vault. If the persist fails the
  engine rolls the password back. If *both* the persist and the rollback fail
  (extremely unlikely), the response says so — reset the admin password manually
  and re-seed `config`.

## Development

```sh
make build      # build the plugin binary into ./dist
make test       # unit tests (race)
make e2e        # full lifecycle vs mock Gitea on Vault + OpenBao (Docker)
make rpm        # build Vault + OpenBao RPMs via nfpm
```

Releases are tag-driven (`make patch|minor|major`): a Woodpecker pipeline builds
the Vault and OpenBao RPMs and uploads them to the internal artifactapi yum repo.

## Deployment (out of scope for this repo; documented for the operator)

Live registration in the real cluster is done exactly like the sibling
`vault-plugin-secrets-rancher` engine. The steps, in order:

1. **Repos**: the Gitea repo is provisioned via `terraform-git`
   (`config/git.unkin.net/unkin/repository/vault-plugin-secrets-gitea.yaml`) →
   PR → plan/apply. After the repo auto-inits, enable its Woodpecker webhook
   manually in the Woodpecker UI (new repos get no webhook automatically) so the
   required `pre-commit`/`build`/`test` checks run.
2. **Release**: tag `v0.1.0` (`make minor` from `v0.0.0`). CI publishes
   `rpm-internal/files/Packages/{vault,openbao}-plugin-secrets-gitea-<ver>-1.x86_64.rpm`.
3. **terraform-vault** (mirrors the rancher wiring — merge in this order):
   - `policies/gitea/admin.yaml` — deployer catalog + engine grant.
   - `config/plugins/vault-plugin-secrets-gitea.yaml` — plugin catalog entry
     with the release `sha256`.
   - `puppet-prod`: add `openbao-plugin-secrets-gitea` (pinned to the exact
     version) to `profiles::packages::include` on the vault storage role.
   - `gitea_secret_backend` + `gitea_secret_backend_role` module instances and
     their config yamls (via the companion `terraform-provider-giteavaultsecret`).
4. **Seed the admin credential in KV** before the backend module applies, e.g.
   `kv/service/vault/au/syd1/secret_backend/gitea/config` with
   `admin_username` + `admin_password` for a purpose-built Gitea site-admin bot
   account (2FA disabled). Then run `vault write -f gitea/config/rotate-root` so
   the standing seed password is replaced by one only Vault holds.
5. **Reload after a binary upgrade**:
   `vault write sys/plugins/reload/backend plugin=vault-plugin-secrets-gitea`
   (the `vault plugin reload -plugin=…` CLI form is broken on this OpenBao).
   Bump the RPM version in puppet-prod and the catalog `sha256` in terraform-vault
   *together* so the on-disk binary stays in lockstep with the catalog.
