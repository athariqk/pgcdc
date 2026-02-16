# Production-ready Dockerfile for pgcdc (Go CDC service)
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o pgcdc .

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/pgcdc ./pgcdc
COPY schema.example.yaml ./schema.example.yaml
COPY .env.example ./
COPY docker-entrypoint.sh ./docker-entrypoint.sh
RUN chmod +x ./docker-entrypoint.sh
RUN adduser -D -u 10001 pgcdcuser
USER pgcdcuser
EXPOSE 8080
ENTRYPOINT ["./docker-entrypoint.sh"]
