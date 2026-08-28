# SPDX-License-Identifier: Apache-2.0
FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS test
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
