# The Go binary is built by CircleCI (architect/go-build), which cross-compiles
# it natively for every architecture and attaches it to the build context as
# mcp-observability-platform-<os>-<arch>. This image only assembles the runtime,
# so multi-arch image builds need no QEMU emulation.
# For a local build, produce the binary first:
#   CGO_ENABLED=0 go build -o mcp-observability-platform-linux-amd64 .
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETOS
ARG TARGETARCH
COPY mcp-observability-platform-${TARGETOS}-${TARGETARCH} /usr/local/bin/mcp-observability-platform

USER nonroot:nonroot
EXPOSE 8080 9091
ENTRYPOINT ["/usr/local/bin/mcp-observability-platform"]
CMD ["serve"]
