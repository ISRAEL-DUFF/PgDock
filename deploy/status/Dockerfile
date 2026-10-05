# pgdock-status, PGDock's public status page (V3 §2.6). Run it somewhere
# other than PGDock's own servers (another provider), behind TLS.
#
#   docker build -f deploy/status/Dockerfile -t pgdock-status .
#
# Behind a TLS-intercepting proxy, pass its CA as a build secret
# (--secret id=ca_bundle,src=ca.pem); base images can come from a mirror
# (--build-arg REGISTRY=mirror.gcr.io/library/).

ARG REGISTRY=
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot
FROM ${REGISTRY}golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -f /run/secrets/ca_bundle ]; then export SSL_CERT_FILE=/run/secrets/ca_bundle; fi; \
    go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/israel-duff/pgdock/internal/version.Version=${VERSION} -X github.com/israel-duff/pgdock/internal/version.Commit=${COMMIT}" \
      -o /out/pgdock-status ./cmd/status \
 && mkdir -p /out/data

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/pgdock-status /usr/local/bin/pgdock-status
COPY --from=build --chown=65532:65532 /out/data /var/lib/pgdock-status
USER 65532:65532
VOLUME ["/var/lib/pgdock-status"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pgdock-status"]
CMD ["run", "--config", "/etc/pgdock-status/status.toml"]
