# syntax=docker/dockerfile:1
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
RUN apk add --no-cache git
COPY worker/main.go ./worker/
COPY pkg/ ./pkg/
RUN go mod tidy -compat=1.21
RUN CGO_ENABLED=0 go build -o /worker worker/main.go

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /worker /worker
COPY --from=builder /app/pkg ./pkg
EXPOSE 9091
ENTRYPOINT ["/worker"] 