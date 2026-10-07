# syntax=docker/dockerfile:1

# ------------------------------------------------------------
# Build stage
# ------------------------------------------------------------
FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /score-server \
    ./cmd/score-server


# ------------------------------------------------------------
# Runtime stage
# ------------------------------------------------------------
FROM debian:bookworm-slim

WORKDIR /app

COPY --from=builder /score-server /app/score-server

COPY --from=builder /src/models/v1.0.0/model.json /app/models/v1.0.0/model.json

ENV MODEL_PATH=models/v1.0.0/model.json

EXPOSE 8080

ENTRYPOINT ["/app/score-server"]
