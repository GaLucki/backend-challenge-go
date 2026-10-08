FROM golang:1.27.0-bookworm AS test
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=1
CMD ["go", "test", "-race", "./...", "-count=1"]

FROM test AS build
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM debian:bookworm-slim AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/api /usr/local/bin/api
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
