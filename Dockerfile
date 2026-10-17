FROM registry.access.redhat.com/ubi9/ubi-minimal:9.8@sha256:8eb2830d0936237fc13a1f2f7e45aecf90d69043380ad167fad0343632937f41 AS build

RUN microdnf update -y --nodocs && microdnf install ca-certificates --nodocs && microdnf clean all

FROM registry.access.redhat.com/ubi9/ubi-micro:9.8@sha256:7e7f79ab747bf2b452e3043dd89f388e92be4c7fdcc8b815b58adf6c99c39c95

LABEL maintainer="Ruohang Feng (@Vonng) <rh@vonng.com>" \
      org.opencontainers.image.vendor="PGSTY" \
      org.opencontainers.image.source="https://github.com/pgsty/kes" \
      org.opencontainers.image.url="https://github.com/pgsty/kes" \
      org.opencontainers.image.description="KES key encryption service, a community-maintained fork of MinIO KES." \
      org.opencontainers.image.licenses="AGPL-3.0-or-later"


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
