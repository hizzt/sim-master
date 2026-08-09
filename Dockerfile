# syntax=docker/dockerfile:1

FROM node:24-bookworm-slim AS frontend
WORKDIR /src
COPY VERSION ./VERSION
COPY frontend/package.json frontend/pnpm-lock.yaml ./frontend/
RUN corepack enable && corepack prepare pnpm@9 --activate \
    && cd frontend && pnpm install --frozen-lockfile
COPY static ./static
COPY frontend ./frontend
RUN cd frontend && pnpm run build

FROM rust:1.90-bookworm AS backend
WORKDIR /src
COPY VERSION ./VERSION
COPY backend ./backend
RUN cd backend && cargo build --release --locked

FROM golang:1.26-bookworm AS vowifi-helper
WORKDIR /src/vowifi-helper
COPY vowifi-helper/go.mod vowifi-helper/go.sum ./
COPY vowifi-helper ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /simadmin-vowifi-helper ./

# This target is used by the release workflow to export architecture-specific
# binaries and static assets without requiring a registry push.
FROM scratch AS artifacts
COPY --from=backend /src/backend/target/release/simadmin /simadmin
COPY --from=vowifi-helper /simadmin-vowifi-helper /simadmin-vowifi-helper
COPY --from=frontend /src/frontend/dist /www
COPY LICENSE /LICENSE
COPY vowifi-helper/THIRD_PARTY_NOTICES.md /THIRD_PARTY_NOTICES.md

FROM debian:bookworm-slim AS runtime
ENV HOST=0.0.0.0 \
    PORT=3000 \
    DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket \
    RUST_LOG=info
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl dbus iproute2 iptables \
       modemmanager network-manager libqmi-utils procps unzip \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/simadmin
COPY --from=artifacts /simadmin /usr/local/lib/sim-master/simadmin
COPY --from=artifacts /simadmin-vowifi-helper /usr/local/lib/sim-master/simadmin-vowifi-helper
COPY --from=artifacts /www /usr/local/lib/sim-master/www
COPY --from=artifacts /LICENSE /usr/local/lib/sim-master/LICENSE
COPY --from=artifacts /THIRD_PARTY_NOTICES.md /usr/local/lib/sim-master/THIRD_PARTY_NOTICES.md
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/lib/sim-master/simadmin /usr/local/lib/sim-master/simadmin-vowifi-helper /usr/local/bin/docker-entrypoint.sh
VOLUME ["/opt/simadmin"]
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/opt/simadmin/simadmin"]
