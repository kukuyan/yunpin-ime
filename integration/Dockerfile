# SPDX-License-Identifier: Apache-2.0
FROM golang:1.24.6-alpine@sha256:c8c5f95d64aa79b6547f3b626eb84b16a7ce18a139e3e9ca19a8c078b85ba80d AS test
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
