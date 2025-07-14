FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
RUN apk add --no-cache git
COPY api/main.go ./api/
COPY pkg/ ./pkg/
RUN go mod tidy -compat=1.21
RUN CGO_ENABLED=0 go build -o /api api/main.go

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /api /api
COPY --from=builder /app/pkg ./pkg
ENV GIN_MODE=release
EXPOSE 8080 8081
ENTRYPOINT ["/api"] 