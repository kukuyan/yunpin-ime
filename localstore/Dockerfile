# SPDX-License-Identifier: Apache-2.0
FROM golang:1.27rc2-alpine@sha256:dcbb18cc5fa1082364dc6aa95224b6b55429d09cbb9631a053d8064c1c367300 AS test
WORKDIR /src
COPY protocol/go.mod protocol/go.sum /src/protocol/
RUN cd /src/protocol && go mod download
COPY localstore/go.mod localstore/go.sum /src/localstore/
RUN cd /src/localstore && go mod download
COPY protocol /src/protocol
COPY localstore /src/localstore
RUN cd /src/localstore && CGO_ENABLED=0 go test ./...
