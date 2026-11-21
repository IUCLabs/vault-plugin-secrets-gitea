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

Release assets download through artifactapi's GitHub proxy, which serves
`https://artifactapi.k8s.syd1.au.unkin.net/api/v1/remote/github/IUCLabs/vault-plugin-secrets-gitea/releases/download/<tag>/<file>`.
Each method below ends with `SHA256` set for [registration](#register-and-enable).
With OpenBao, use `bao` in place of `vault`.

```sh
TAG=vX.Y.Z  # the release to install
V=${TAG#v}
BASE=https://artifactapi.k8s.syd1.au.unkin.net/api/v1/remote/github/IUCLabs/vault-plugin-secrets-gitea/releases/download/$TAG
```

### RPM

`vault-plugin-secrets-gitea` installs `/usr/libexec/vault-plugins/vault-plugin-secrets-gitea`;
`openbao-plugin-secrets-gitea` installs it in `/usr/libexec/openbao-plugins/`.
Set the server's `plugin_directory` to that directory.

```sh
dnf install vault-plugin-secrets-gitea  # from a yum repo carrying it, or from the release:
dnf install "$BASE/vault-plugin-secrets-gitea-$V-1.x86_64.rpm"  # openbao-plugin-secrets-gitea-$V-1.x86_64.rpm for OpenBao

SHA256=$(sha256sum /usr/libexec/vault-plugins/vault-plugin-secrets-gitea | cut -d' ' -f1)
```

### Released binary

`vault-plugin-secrets-gitea_<version>_SHA256SUMS` lists the binary and the RPMs, and
`SHA256SUMS.sig` is its detached signature by the IUCLabs release GPG key.

```sh
curl -fLO "$BASE/vault-plugin-secrets-gitea_${V}_linux_amd64"
curl -fLO "$BASE/vault-plugin-secrets-gitea_${V}_SHA256SUMS"
curl -fLO "$BASE/vault-plugin-secrets-gitea_${V}_SHA256SUMS.sig"
gpg --verify "vault-plugin-secrets-gitea_${V}_SHA256SUMS.sig" "vault-plugin-secrets-gitea_${V}_SHA256SUMS"
sha256sum --check --ignore-missing "vault-plugin-secrets-gitea_${V}_SHA256SUMS"

install -m 0755 "vault-plugin-secrets-gitea_${V}_linux_amd64" <plugin_directory>/vault-plugin-secrets-gitea
SHA256=$(sha256sum <plugin_directory>/vault-plugin-secrets-gitea | cut -d' ' -f1)
```

### Kubernetes image volume

`ghcr.io/iuclabs/vault-plugin-secrets-gitea:<tag>` is a `FROM scratch` image holding only
`/vault-plugin-secrets-gitea`. Mount it read-only into the Vault/OpenBao container with an
[image volume](https://kubernetes.io/docs/concepts/storage/volumes/#image):
Kubernetes 1.31+ with the `ImageVolume` feature gate (beta, on by default from
1.33). Vault and OpenBao only run a binary that sits directly in
`plugin_directory`, not in a subdirectory, so mount the file itself with
`subPath`, which on image volumes needs 1.33+.

```yaml
spec:
  containers:
    - name: vault
      volumeMounts:
        - name: plugin-gitea
          mountPath: /vault/plugins/vault-plugin-secrets-gitea  # <plugin_directory>/vault-plugin-secrets-gitea
          subPath: vault-plugin-secrets-gitea
          readOnly: true
  volumes:
    - name: plugin-gitea
      image:
        reference: ghcr.io/iuclabs/vault-plugin-secrets-gitea:vX.Y.Z
        pullPolicy: IfNotPresent
```

The image holds the released binary, so its sha256 is the `linux_amd64` line
of `SHA256SUMS`:

```sh
SHA256=$(curl -fsSL "$BASE/vault-plugin-secrets-gitea_${V}_SHA256SUMS" | awk '/_linux_amd64$/ {print $1}')
```

### From source

```sh
make build  # writes ./dist/vault-plugin-secrets-gitea
```

Copy it into `plugin_directory` and take `SHA256` from `sha256sum` as above.

### Register and enable

```sh
vault plugin register -sha256="$SHA256" secret vault-plugin-secrets-gitea
vault secrets enable -path=gitea vault-plugin-secrets-gitea
```

After replacing the binary, register the new sha256 and reload the plugin:

```sh
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
