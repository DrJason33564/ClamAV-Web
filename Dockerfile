FROM --platform=$BUILDPLATFORM node:lts-bookworm-slim AS web-builder

WORKDIR /src/ClamAV-Web
COPY ClamAV-Web/package.json ClamAV-Web/package-lock.json ./
RUN npm ci
COPY ClamAV-Web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:bookworm AS scanner-builder

WORKDIR /src/server
ARG TARGETOS
ARG TARGETARCH
COPY server/go.mod ./
COPY server/*.go ./
COPY --from=web-builder /src/ClamAV-Web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/server .

FROM clamav/clamav:stable_base-debian

USER 0

RUN apt-get update \
    && apt-get install -y --no-install-recommends cron ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY startup.sh /startup.sh
COPY log.sh /log.sh
COPY config.sh /config.sh
COPY cron.sh /cron.sh
COPY exclude.sh /exclude.sh
COPY scan_once.sh /scan_once.sh
COPY cron_scan_example.conf /cron_scan_example.conf
COPY clamd.conf /clamd.conf
COPY --from=scanner-builder /out/server /server

RUN sed -i 's/\r$//' /startup.sh /log.sh /config.sh /cron.sh /exclude.sh /scan_once.sh /cron_scan_example.conf /clamd.conf \
    && chmod +x /startup.sh /log.sh /config.sh /cron.sh /exclude.sh /scan_once.sh \
    && chmod +x /server \
    && mkdir -p /config /scan /quarantine /log /state /state/jobs /var/log/clamav \
    && chmod 755 /config /scan /quarantine /log /state /state/jobs \
    && install -o root -g root -m 0644 /clamd.conf /etc/clamav/clamd.conf

EXPOSE 8080

HEALTHCHECK NONE

ENTRYPOINT ["/startup.sh"]
