# SPDX-License-Identifier: Apache-2.0
FROM golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS test
WORKDIR /src
COPY protocol/go.mod protocol/go.sum /src/protocol/
RUN cd /src/protocol && go mod download
COPY localstore/go.mod localstore/go.sum /src/localstore/
RUN cd /src/localstore && go mod download
COPY protocol /src/protocol
COPY localstore /src/localstore
RUN cd /src/localstore && CGO_ENABLED=0 go test ./...
