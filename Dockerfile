FROM debian:bookworm-slim AS certs
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY send2kindle-linux-amd64 /send2kindle
USER 1000:1000
ENTRYPOINT ["/send2kindle"]
