# xenibase-edge, backend services' gateway (V4 §2.1): one per region, on the
# region's nodes. Stateless; it follows xenibase-server's configuration feed.
#
#   docker build -f deploy/edge/Dockerfile -t xenibase-edge .
#
# Behind a TLS-intercepting proxy, pass its CA as a build secret
# (--secret id=ca_bundle,src=ca.pem); base images can come from a mirror
# (--build-arg REGISTRY=mirror.gcr.io/library/).

ARG REGISTRY=
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot
FROM ${REGISTRY}golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -f /run/secrets/ca_bundle ]; then export SSL_CERT_FILE=/run/secrets/ca_bundle; fi; \
    go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/israel-duff/xenibase/internal/version.Version=${VERSION} -X github.com/israel-duff/xenibase/internal/version.Commit=${COMMIT}" \
      -tags nodynamic \
      -o /out/xenibase-edge ./cmd/edge

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/xenibase-edge /usr/local/bin/xenibase-edge
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/xenibase-edge"]
CMD ["run"]
