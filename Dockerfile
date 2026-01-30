FROM golang:1.24 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/bin/server .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /app/bin/server /app/server
COPY --from=builder /app/assets /app/assets
COPY --from=builder /app/prompts /app/prompts

EXPOSE 8000

CMD ["/app/server"]
