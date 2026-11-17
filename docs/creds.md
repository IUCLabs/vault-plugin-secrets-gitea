# Credentials

Reading `creds/<role>` mints a new Gitea access token for the role's user. The
token is tied to a Vault lease and deleted from Gitea when the lease is revoked
or reaches its max TTL.

Gitea tokens have no expiry of their own, so the lease is the only thing that
ends a token's life. Revocation calls
`DELETE /api/v1/users/{username}/tokens/{id}`; a token that is already gone is
treated as revoked.

Examples assume the engine is mounted at `gitea/`. With OpenBao, use `bao` in
place of `vault`.

## Mint a token

| Method | Path                |
|--------|---------------------|
| `GET`  | `gitea/creds/:name` |

`POST` to the same path behaves the same.

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | yes | Role name (in the URL). |

### Response

| Field | Description |
|-------|-------------|
| `token` | The Gitea access token. |
| `token_id` | Gitea's ID for the token. |
| `token_name` | Token name in Gitea, `<prefix>-<role>-<random>`. |
| `username` | User the token belongs to. |
| `gitea_url` | Gitea base URL from `config`. |
| `scopes` | Scopes granted to the token. |

### Example

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

Use the token like any Gitea access token:

```sh
curl -H "Authorization: token $TOKEN" https://gitea.example.com/api/v1/user
```

## Renew and revoke

Renewal extends the lease up to the role's `max_ttl`; the token itself does not
change.

```sh
vault lease renew gitea/creds/ci-bot/3Yb8...
vault lease revoke gitea/creds/ci-bot/3Yb8...
```

Revoke every token minted from a role:

```sh
vault lease revoke -prefix gitea/creds/ci-bot
```
