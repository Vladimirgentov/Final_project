# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS builder
WORKDIR /src

# Нужны для сборки некоторых модулей и git-fetch при необходимости
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bin/prices-service .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && update-ca-certificates

WORKDIR /app
COPY --from=builder /bin/prices-service /app/prices-service

# Дефолты соответствуют требованиям задания
ENV POSTGRES_HOST=project-sem1-postgres \
    POSTGRES_PORT=5432 \
    POSTGRES_USER=validator \
    POSTGRES_PASSWORD=val1dat0r \
    POSTGRES_DB=project-sem-1 \
    HTTP_ADDR=:8080

EXPOSE 8080
ENTRYPOINT ["/app/prices-service"]
