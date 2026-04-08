FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o flux .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /etc/flux
COPY --from=builder /app/flux /usr/local/bin/flux
EXPOSE 7227
ENTRYPOINT ["flux"]
