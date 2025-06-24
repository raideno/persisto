FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY src/ ./src/

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./src/main.go

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /app/main /main

EXPOSE 8080

ENTRYPOINT ["/main"]