# Caddy with the Cloudflare DNS plugin, for the wildcard certificate of
# backend services' API domain (*.api.example.com), which only a DNS challenge
# can get. Built by enable-services.sh; the stock caddy:2 image does the rest
# of Xenibase's TLS and stays the default.
ARG CADDY_VERSION=2
FROM caddy:${CADDY_VERSION}-builder AS build
RUN xcaddy build --with github.com/caddy-dns/cloudflare

FROM caddy:${CADDY_VERSION}
COPY --from=build /usr/bin/caddy /usr/bin/caddy
