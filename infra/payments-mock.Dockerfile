# Builds cmd/payments-mock. The build context is the repo root (see docker-compose.yml).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /payments-mock ./cmd/payments-mock

# Alpine rather than scratch, so the healthcheck has wget.
FROM alpine:3.22
COPY --from=build /payments-mock /usr/local/bin/payments-mock
EXPOSE 12111
ENTRYPOINT ["payments-mock"]
