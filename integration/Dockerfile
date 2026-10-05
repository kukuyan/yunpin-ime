# SPDX-License-Identifier: Apache-2.0
FROM golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS test
WORKDIR /src
COPY protocol/go.mod protocol/go.sum /src/protocol/
RUN cd /src/protocol && go mod download
COPY localstore/go.mod localstore/go.sum /src/localstore/
RUN cd /src/localstore && go mod download
COPY sync/go.mod sync/go.sum /src/sync/
RUN cd /src/sync && go mod download
COPY syncclient/go.mod syncclient/go.sum /src/syncclient/
RUN cd /src/syncclient && go mod download
COPY desktopagent/go.mod desktopagent/go.sum /src/desktopagent/
RUN cd /src/desktopagent && go mod download
COPY integration/go.mod integration/go.sum /src/integration/
RUN cd /src/integration && go mod download
COPY protocol /src/protocol
COPY localstore /src/localstore
COPY sync /src/sync
COPY syncclient /src/syncclient
COPY desktopagent /src/desktopagent
COPY integration /src/integration
RUN cd /src/syncclient && CGO_ENABLED=0 go test ./... && \
    cd /src/desktopagent && CGO_ENABLED=0 go test ./... && \
    cd /src/integration && CGO_ENABLED=0 go test ./...
