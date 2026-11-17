# Config

The `config` path holds the Gitea connection and the seeded site-admin
credentials the engine uses to mint and delete tokens. `config/rotate-root`
replaces the seeded password with one only Vault knows.

Examples assume the engine is mounted at `gitea/`. With OpenBao, use `bao` in
place of `vault`.

## Configure the connection

| Method | Path           |
|--------|----------------|
| `POST` | `gitea/config` |

On write, the engine authenticates to Gitea with the supplied credentials and
checks that the user is a site admin. A bad URL, username or password fails the
write and nothing is stored.

### Parameters

| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `gitea_url` | string | yes | | Base URL of the Gitea server, e.g. `https://gitea.example.com`. |
| `admin_username` | string | yes | | Gitea site-admin username. Its Basic-Auth credentials mint and delete tokens for other users. |
| `admin_password` | string | yes | | Password for `admin_username`. Write-only, never returned. |
| `admin_login_name` | string | no | `admin_username` | `login_name` sent to Gitea's admin edit API during `rotate-root`. Set it for admins from an external auth source. |
| `admin_source_id` | int | no | `0` | Authentication source ID of the admin, sent during `rotate-root`. `0` means a local user. |
| `ca_cert` | string | no | | PEM CA certificate that signed Gitea's TLS certificate. Write-only. |
| `tls_skip_verify` | bool | no | `false` | Skip TLS verification of the Gitea server. |
| `request_timeout_seconds` | int | no | `30` | HTTP timeout for calls to Gitea. |

On update, omitted parameters keep their stored values.

### Example

```sh
vault write gitea/config \
  gitea_url=https://gitea.example.com \
  admin_username=vault-admin \
  admin_password="$GITEA_ADMIN_PASSWORD" \
  ca_cert=@gitea-ca.pem
```

## Read the config

| Method | Path           |
|--------|----------------|
| `GET`  | `gitea/config` |

Returns every parameter except `admin_password` and `ca_cert`.

```sh
vault read gitea/config
```

```text
Key                        Value
---                        -----
admin_login_name
admin_source_id            0
admin_username             vault-admin
gitea_url                  https://gitea.example.com
request_timeout_seconds    30
tls_skip_verify            false
```

## Delete the config

| Method   | Path           |
|----------|----------------|
| `DELETE` | `gitea/config` |

```sh
vault delete gitea/config
```

Outstanding leases can no longer be revoked against Gitea until the config is
written again.

## Rotate the admin password

| Method | Path                       |
|--------|----------------------------|
| `POST` | `gitea/config/rotate-root` |

Generates a random password, sets it on Gitea via
`PATCH /api/v1/admin/users/{admin_username}` using the current credentials, then
stores it. After rotation the old password no longer works.

Constraints:

- The admin must be a local Gitea user (or set `admin_login_name` and
  `admin_source_id` to match its auth source).
- The admin must not have 2FA enabled; the engine uses Basic Auth.
- Gitea is changed first, then the new password is stored. If storing fails the
  engine sets Gitea back to the old password. If that rollback also fails the
  error says so, and the admin password must be reset by hand and written to
  `config` again.

```sh
vault write -f gitea/config/rotate-root
```

```text
Key               Value
---               -----
admin_username    vault-admin
rotated           true
```
