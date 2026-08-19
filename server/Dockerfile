# Build context is the repository root: server and client share internal/protocol.
FROM golang:1.26.6-alpine3.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ ./internal/
COPY server/ ./server/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/beacon ./server

FROM scratch
COPY --from=build /out/beacon /beacon
EXPOSE 8080
# scratch has no shell and no curl, so the binary probes itself
HEALTHCHECK --interval=10s --timeout=3s --start-period=3s --retries=3 CMD ["/beacon", "healthcheck"]
ENTRYPOINT ["/beacon"]
CMD ["server"]
