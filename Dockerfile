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
COPY schema.yaml ./schema.yaml
COPY .env* ./
RUN adduser -D -u 10001 pgcdcuser
USER pgcdcuser
EXPOSE 8080
ENTRYPOINT ["./pgcdc"]
