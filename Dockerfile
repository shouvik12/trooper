# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod ./
COPY main.go ./
COPY providers.go ./

RUN go build -o trooper .

# ── Run stage ─────────────────────────────────────────────────────────────────
FROM alpine:3.19

WORKDIR /app
COPY --from=builder /app/trooper .

EXPOSE 3000

ENTRYPOINT ["./trooper"]
