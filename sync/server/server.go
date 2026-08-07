// SPDX-License-Identifier: Apache-2.0

// Package server exposes the encrypted relay for integration tests and
// embedders while keeping implementation details under internal/server.
package server

import (
	"context"
	"io"

	internal "github.com/kukuyan/yunpin-ime/sync/internal/server"
)

type Server = internal.Server

func New(ctx context.Context, databasePath string, logOutput io.Writer) (*Server, error) {
	return internal.New(ctx, databasePath, logOutput)
}
