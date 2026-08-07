# SPDX-License-Identifier: Apache-2.0
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS test
WORKDIR /src
COPY protocol/go.mod protocol/go.sum /src/protocol/
RUN cd /src/protocol && go mod download
COPY localstore/go.mod localstore/go.sum /src/localstore/
RUN cd /src/localstore && go mod download
COPY protocol /src/protocol
COPY localstore /src/localstore
RUN cd /src/localstore && CGO_ENABLED=0 go test ./...
