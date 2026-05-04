# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod ./
COPY main.go ./
COPY providers.go ./
COPY classifier.go ./

RUN go build -o trooper .

# ── Run stage ─────────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /app/trooper .

EXPOSE 3000

ENTRYPOINT ["./trooper"]
