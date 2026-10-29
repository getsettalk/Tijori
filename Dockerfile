# syntax=docker/dockerfile:1
# Tijori container image.
#   docker build -t tijori .                    # small image (distroless, no ffmpeg)
#   docker build -t tijori:ffmpeg --target ffmpeg .   # Alpine image with ffmpeg for video

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev COMMIT=none DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN VERSION="${VERSION#v}" && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/getsettalk/Tijori/internal/version.Version=${VERSION} -X github.com/getsettalk/Tijori/internal/version.Commit=${COMMIT} -X github.com/getsettalk/Tijori/internal/version.Date=${DATE}" \
      -o /out/tijori ./cmd/tijori \
 && mkdir -p /out/data

# Default image: static binary on distroless, runs as non-root (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot AS default
LABEL org.opencontainers.image.title="Tijori" \
      org.opencontainers.image.description="Tiny self-hosted S3-compatible object storage in one binary" \
      org.opencontainers.image.source="https://github.com/getsettalk/Tijori" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.artifacthub.package.logo-url="https://raw.githubusercontent.com/getsettalk/Tijori/main/web/admin/logo.svg"
COPY --from=build /out/tijori /usr/local/bin/tijori
COPY --from=build --chown=65532:65532 /out/data /data
ENV TIJORI_DATA_DIR=/data TIJORI_LISTEN=:9000
EXPOSE 9000
VOLUME /data
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/tijori", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/tijori"]
CMD ["serve"]

# Variant with ffmpeg for video processing.
FROM alpine:3.22 AS ffmpeg
LABEL org.opencontainers.image.title="Tijori (with ffmpeg)" \
      org.opencontainers.image.source="https://github.com/getsettalk/Tijori" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.artifacthub.package.logo-url="https://raw.githubusercontent.com/getsettalk/Tijori/main/web/admin/logo.svg"
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && adduser -D -H -u 10001 tijori \
 && mkdir -p /data && chown tijori:tijori /data
COPY --from=build /out/tijori /usr/local/bin/tijori
ENV TIJORI_DATA_DIR=/data TIJORI_LISTEN=:9000
EXPOSE 9000
VOLUME /data
USER tijori
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/tijori", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/tijori"]
CMD ["serve"]

# Plain `docker build .` produces the default image.
FROM default
