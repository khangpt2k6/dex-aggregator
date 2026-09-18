# Build stage.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module
# cache on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Static binary, stripped. CGO off so it runs on distroless without a libc.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/aggregator ./cmd/aggregator

# Run stage. Distroless has no shell and no package manager, which removes most
# of what an attacker would reach for in a container that is only ever meant to
# serve HTTP.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/aggregator /aggregator

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/aggregator"]
