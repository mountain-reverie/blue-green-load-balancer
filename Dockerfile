# Dockerfile for blue-green-load-balancer
# Binaries are pre-built in GitHub Actions and copied here
# This avoids multi-stage builds and maximizes GHA cache efficiency

ARG TARGETARCH=amd64

FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETARCH

# Copy pre-built binaries from dist directory
# These are built per-architecture in the CI workflow
COPY dist/${TARGETARCH}/bluegreen /usr/local/bin/bluegreen
COPY dist/${TARGETARCH}/bgctl /usr/local/bin/bgctl

# Copy static files if any
COPY static/ /app/static/

# Run as non-root user (distroless provides this)
USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/bluegreen"]
CMD ["-config", "/etc/bluegreen/config.yaml"]
