# SPDX-License-Identifier: Apache-2.0
FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS test
WORKDIR /src
COPY protocol/go.mod protocol/go.sum /src/protocol/
RUN cd /src/protocol && go mod download
COPY localstore/go.mod localstore/go.sum /src/localstore/
RUN cd /src/localstore && go mod download
COPY protocol /src/protocol
COPY localstore /src/localstore
RUN cd /src/localstore && CGO_ENABLED=0 go test ./...
