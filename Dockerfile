# syntax=docker/dockerfile:1.6
#
# Two-stage build:
#   1. golang:1.25-alpine compiles a static binary. CGO is off so the
#      resulting binary has zero libc dependency and runs on any
#      linux/amd64 (or arm64 with matching base) userland.
#   2. alpine:3.21 hosts the binary plus root CA bundle (needed for
#      the outbound HTTPS calls to Supabase Auth). No shell, no
#      package manager beyond what alpine already ships.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Copy module manifests first so `go mod download` is cacheable — a
# rebuild that only changes source files skips the network step.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -s -w strips the DWARF symbol tables (~30-40% size reduction) with
# no functional impact. CGO_ENABLED=0 guarantees a static binary.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api ./cmd/api


FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/api /app/api
EXPOSE 8080 
ENTRYPOINT ["/app/api"] 
