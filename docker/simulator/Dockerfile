FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN apk add --no-cache git
COPY . .
RUN go mod tidy -compat=1.21
WORKDIR /app/simulator
RUN go build -o simulator main.go

FROM alpine:latest
WORKDIR /root/
COPY --from=builder /app/simulator/simulator .
CMD ["./simulator"] 