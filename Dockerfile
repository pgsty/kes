FROM registry.access.redhat.com/ubi9/ubi-minimal:9.8@sha256:c4c285d42941ebb2bd4da878479008e1e4536bb599aad1dbe568f89de9053661 AS build

# Native root blocklist mitigation: https://access.redhat.com/security/vulnerabilities/RHSB-2026-012
RUN microdnf update -y --nodocs && \
    microdnf install ca-certificates --nodocs && \
    if [ -d /etc/pki/ca-trust/source/blocklist ]; then \
      blocklist=/etc/pki/ca-trust/source/blocklist; \
    else \
      blocklist=/etc/pki/ca-trust/source/blacklist; \
    fi && \
    trust extract --overwrite --format=pem-bundle \
      --filter='pkcs11:object=Comodo%20AAA%20Services%20root;type=cert' \
      "$blocklist/Comodo_AAA_Services_root.pem" && \
    trust extract --overwrite --format=pem-bundle \
      --filter='pkcs11:object=emSign%20ECC%20Root%20CA%20-%20G3;type=cert' \
      "$blocklist/emSign_ECC_Root_CA_-_G3.pem" && \
    update-ca-trust && \
    microdnf clean all

FROM registry.access.redhat.com/ubi9/ubi-micro:9.8@sha256:5be877a2a280ae36eb9ab80ca4efb28bbf77c57abbba38137cdc37a03b8465e6

LABEL maintainer="Ruohang Feng (@Vonng) <rh@vonng.com>" \
      org.opencontainers.image.title="PGSTY KES" \
      org.opencontainers.image.vendor="PGSTY" \
      org.opencontainers.image.source="https://github.com/pgsty/kes" \
      org.opencontainers.image.url="https://github.com/pgsty/kes" \
      org.opencontainers.image.description="PGSTY KES key encryption service." \
      org.opencontainers.image.licenses="AGPL-3.0-only"


# On RHEL the certificate bundle is located at:
# - /etc/pki/tls/certs/ca-bundle.crt (RHEL 6)
# - /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem (RHEL 7)
COPY --from=build /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem /etc/pki/ca-trust/extracted/pem/

COPY LICENSE /LICENSE
COPY CREDITS /CREDITS
COPY NOTICE /NOTICE
COPY kes /kes

# KES is designed to run unprivileged; 1000:1000 matches the securityContext
# the operator applies to spec.kes pods, so the image default and the
# deployed reality agree instead of defaulting to root.
USER 1000:1000

EXPOSE 7373

ENTRYPOINT ["/kes"]
CMD ["--help"]
