# SPDX-License-Identifier: Apache-2.0

FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS dependencies
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
