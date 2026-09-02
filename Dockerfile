FROM golang:1.21-alpine AS builder

ARG MATCHA_VERSION=v0.9.0

RUN apk add --no-cache git

WORKDIR /build
RUN git clone --depth 1 --branch ${MATCHA_VERSION} https://github.com/piqoni/matcha.git
WORKDIR /build/matcha
RUN go build -o matcha .

# The webapp needs a newer toolchain than matcha does (golang.org/x/crypto
# requires it). The two stages are independent, so this does not affect the
# matcha build above. GOTOOLCHAIN=local makes a version mismatch a loud build
# failure instead of a silent toolchain download.
FROM golang:1.25-alpine AS webapp-builder

ENV GOTOOLCHAIN=local

WORKDIR /app/webapp
COPY webapp/go.mod webapp/go.sum ./
RUN go mod download

COPY webapp/*.go ./
# Templates are embedded with //go:embed, so they must be present at compile
# time. COPY webapp/*.go above does not pick up subdirectories.
COPY webapp/templates ./templates
RUN CGO_ENABLED=0 go build -o webapp .

FROM alpine:3.19

WORKDIR /app

COPY --from=builder /build/matcha/matcha /usr/local/bin/matcha
COPY --from=webapp-builder /app/webapp/webapp /usr/local/bin/webapp

RUN mkdir -p /app/output /app/config /app/webapp/static
COPY webapp/static /app/webapp/static

COPY webapp/matcha-runner.sh /usr/local/bin/matcha-runner
RUN chmod +x /usr/local/bin/matcha-runner

COPY webapp/entrypoint.sh /app/webapp/entrypoint.sh
RUN chmod +x /app/webapp/entrypoint.sh

EXPOSE 7321

ENTRYPOINT ["/app/webapp/entrypoint.sh"]
