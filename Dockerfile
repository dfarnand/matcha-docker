FROM golang:1.21-alpine AS builder

ARG MATCHA_VERSION=v0.9.0

RUN apk add --no-cache git

WORKDIR /build
RUN git clone --depth 1 --branch ${MATCHA_VERSION} https://github.com/piqoni/matcha.git
WORKDIR /build/matcha
RUN go build -o matcha .

FROM golang:1.21-alpine AS webapp-builder

WORKDIR /app/webapp
COPY webapp/go.mod webapp/go.sum ./
RUN go mod download

COPY webapp/*.go ./
RUN CGO_ENABLED=0 go build -o webapp .

FROM alpine:3.19

RUN apk add --no-cache dcron

WORKDIR /app

COPY --from=builder /build/matcha/matcha /usr/local/bin/matcha
COPY --from=webapp-builder /app/webapp/webapp /usr/local/bin/webapp

RUN mkdir -p /app/output /app/webapp/static
COPY webapp/static /app/webapp/static

COPY webapp/entrypoint.sh /app/webapp/entrypoint.sh
RUN chmod +x /app/webapp/entrypoint.sh

EXPOSE 8080

ENTRYPOINT ["/app/webapp/entrypoint.sh"]
