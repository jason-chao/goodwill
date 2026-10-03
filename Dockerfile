# Builds a small image holding only the goodwill binary.
#
#   docker build -t goodwill .
#   docker run -d --name goodwill -v goodwill-data:/data \
#     -p 127.0.0.1:8080:8080 -p 127.0.0.1:8081:8081 goodwill
#
# See docs/install.md for the details.

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# The country database is downloaded unless the build context already has one.
RUN [ -f internal/geo/data/country.mmdb.gz ] || ./scripts/fetch-geo.sh

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -tags embedgeo \
        -ldflags "-s -w -X main.version=${VERSION}" -o /out/goodwill ./cmd/goodwill \
    && mkdir -p /out/data /out/tmp \
    && chmod 1777 /out/tmp

FROM scratch
COPY --from=build /out/goodwill /goodwill
# An empty data directory owned by the runtime user, so a fresh volume is
# writable, and a /tmp for unpacking the country database.
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/tmp /tmp

USER 65532:65532
ENV GOODWILL_DATABASE_PATH=/data/goodwill.db \
    GOODWILL_PUBLIC_LISTEN=0.0.0.0:8080 \
    GOODWILL_ADMIN_LISTEN=0.0.0.0:8081
VOLUME /data
EXPOSE 8080 8081
HEALTHCHECK --interval=60s --timeout=5s --start-period=10s CMD ["/goodwill", "health"]
ENTRYPOINT ["/goodwill"]
CMD ["serve"]
