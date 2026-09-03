FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# COVER=1 builds a coverage-instrumented binary, so the integration suite --
# which exercises send2kindle as a real container against a real SMTP server --
# can report coverage that unit tests structurally cannot reach
# (go.dev/blog/integration-test-coverage). Run it with GOCOVERDIR pointed at a
# mounted directory. Unset, this is an ordinary build.
ARG COVER=0
RUN if [ "$COVER" = "1" ]; then \
      CGO_ENABLED=0 go build -cover -coverpkg=./... -trimpath -o /send2kindle .; \
    else \
      CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /send2kindle .; \
    fi

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /send2kindle /send2kindle
USER 1000:1000
ENTRYPOINT ["/send2kindle"]
