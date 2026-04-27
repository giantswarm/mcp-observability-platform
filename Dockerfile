FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X github.com/giantswarm/mcp-observability-platform/cmd.version=${VERSION}" \
    -o /out/mcp-observability-platform .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/mcp-observability-platform /usr/local/bin/mcp-observability-platform
USER nonroot:nonroot
EXPOSE 8080 9091
ENTRYPOINT ["/usr/local/bin/mcp-observability-platform"]
CMD ["serve"]
