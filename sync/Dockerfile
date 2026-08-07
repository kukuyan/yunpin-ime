# SPDX-License-Identifier: Apache-2.0

FROM golang:1.24.6-alpine@sha256:c8c5f95d64aa79b6547f3b626eb84b16a7ce18a139e3e9ca19a8c078b85ba80d AS dependencies
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM dependencies AS source
COPY . .

FROM source AS test
RUN CGO_ENABLED=0 go test ./...

FROM test AS build
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/yunpin-sync ./cmd/yunpin-sync && \
    mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS runtime
COPY --from=build /out/yunpin-sync /yunpin-sync
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
EXPOSE 8080
ENV YUNPIN_LISTEN=:8080 YUNPIN_DATABASE=/data/yunpin-sync.db
USER nonroot:nonroot
ENTRYPOINT ["/yunpin-sync"]
