FROM debian:bookworm-slim AS certs
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY kindle-sender-linux-amd64 /kindle-sender
USER 1000:1000
ENTRYPOINT ["/kindle-sender"]
