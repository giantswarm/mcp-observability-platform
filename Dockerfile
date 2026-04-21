FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X github.com/giantswarm/mcp-observability-platform/cmd.version=${VERSION}" \
    -o /out/mcp-observability-platform .

FROM gsoci.azurecr.io/giantswarm/alpine:3.20.3-giantswarm
COPY --from=builder /out/mcp-observability-platform /usr/local/bin/mcp-observability-platform
USER 1000:1000
EXPOSE 8080 9091
ENTRYPOINT ["/usr/local/bin/mcp-observability-platform"]
CMD ["serve"]
