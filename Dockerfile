FROM scratch
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/vault-plugin-secrets-gitea /vault-plugin-secrets-gitea
ENTRYPOINT ["/vault-plugin-secrets-gitea"]
