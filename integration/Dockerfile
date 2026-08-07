# SPDX-License-Identifier: Apache-2.0
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS test
WORKDIR /src
COPY protocol/go.mod protocol/go.sum /src/protocol/
RUN cd /src/protocol && go mod download
COPY sync/go.mod sync/go.sum /src/sync/
RUN cd /src/sync && go mod download
COPY integration/go.mod integration/go.sum /src/integration/
RUN cd /src/integration && go mod download
COPY protocol /src/protocol
COPY sync /src/sync
COPY integration /src/integration
RUN cd /src/integration && CGO_ENABLED=0 go test ./...
