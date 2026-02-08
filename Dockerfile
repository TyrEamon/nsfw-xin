FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY . .
RUN go build -o server ./cmd/server

FROM alpine:latest
WORKDIR /app
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/server ./server
COPY --from=builder /app/web ./web
COPY --from=builder /app/schema.sql ./schema.sql

ENV LISTEN_ADDR=:8080
CMD ["./server"]
