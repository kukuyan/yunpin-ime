# SPDX-License-Identifier: Apache-2.0

FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS dependencies
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="$GOPROXY" go mod download

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
