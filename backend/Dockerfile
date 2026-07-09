# syntax=docker/dockerfile:1

# --- build stage ---
# Pin the builder to the host (build) platform and cross-compile to the target
# arch. CGO_ENABLED=0 is pure Go, so cross-compiling is fast and needs no QEMU
# emulation for the build itself (the prod host is t4g.micro / arm64).
FROM --platform=$BUILDPLATFORM golang:1.25.12-alpine AS builder
RUN apk add --no-cache git
WORKDIR /src

ARG TARGETOS
ARG TARGETARCH

# Copy everything. Generate the Ent client FIRST (its schema imports resolve via
# -mod=mod), then tidy so the generated ./ent/* packages exist locally and aren't
# mistaken for remote modules. go.sum is created here on first build.
COPY . .
RUN go generate ./ent
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -ldflags="-s -w" -o /out/api    ./cmd/api  \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -ldflags="-s -w" -o /out/worker ./cmd/worker \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -ldflags="-s -w" -o /out/seed   ./cmd/seed

# --- runtime stage ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=builder /out/ /usr/local/bin/
USER app
EXPOSE 8080
# Override `command:` in compose to run `worker` or `seed`.
CMD ["api"]
