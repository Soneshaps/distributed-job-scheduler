# syntax=docker/dockerfile:1
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
RUN apk add --no-cache git
COPY publisher/main.go ./publisher/
COPY pkg/ ./pkg/
RUN go mod tidy -compat=1.21
RUN CGO_ENABLED=0 go build -o /publisher publisher/main.go

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /publisher /publisher
COPY --from=builder /app/pkg ./pkg
ENTRYPOINT ["/publisher"] 