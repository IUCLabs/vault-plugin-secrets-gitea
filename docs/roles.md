# Roles

A role binds a Gitea user to a set of token scopes and lease TTLs. Each read of
[`creds/<role>`](creds.md) mints a new token for that user.

Examples assume the engine is mounted at `gitea/`. With OpenBao, use `bao` in
place of `vault`.

## Create or update a role

| Method | Path                |
|--------|---------------------|
| `POST` | `gitea/roles/:name` |

### Parameters

| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `name` | string | yes | | Role name (in the URL). Lower-cased. |
| `username` | string | yes | | Gitea user the minted tokens belong to. |
| `scopes` | list of strings | yes | | Gitea token scopes, comma-separated. See [Scopes](#scopes). |
| `token_name_prefix` | string | no | `vault` | Prefix of each token's name in Gitea. Tokens are named `<prefix>-<role>-<random>`. |
| `ttl` | duration | no | mount default | Default lease TTL. |
| `max_ttl` | duration | no | mount max | Maximum lease TTL. `ttl` must not exceed it. |

On update, omitted parameters keep their stored values.

### Scopes

Scopes are validated against Gitea's scope set, lower-cased and de-duplicated:

- `all`, `public-only`
- `read:<category>` and `write:<category>` for each of `activitypub`, `admin`,
  `misc`, `notification`, `organization`, `package`, `issue`, `repository`,
  `user`

`write:` implies `read:` for the same category.

### Example

```sh
vault write gitea/roles/ci-bot \
  username=ci-bot \
  scopes=read:repository,write:issue \
  ttl=1h \
  max_ttl=24h
```

## Read a role

| Method | Path                |
|--------|---------------------|
| `GET`  | `gitea/roles/:name` |

```sh
vault read gitea/roles/ci-bot
```

```text
Key                  Value
---                  -----
max_ttl              86400
scopes               [read:repository write:issue]
token_name_prefix    vault
ttl                  3600
username             ci-bot
```

## List roles

| Method | Path           |
|--------|----------------|
| `LIST` | `gitea/roles`  |

```sh
vault list gitea/roles
```

## Delete a role

| Method   | Path                |
|----------|---------------------|
| `DELETE` | `gitea/roles/:name` |

```sh
vault delete gitea/roles/ci-bot
```

Tokens already minted from the role stay valid until their leases end.
