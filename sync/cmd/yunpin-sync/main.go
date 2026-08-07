// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kukuyan/yunpin-ime/sync/internal/server"
)

func main() {
	listenAddress := envOr("YUNPIN_LISTEN", ":8080")
	databasePath := envOr("YUNPIN_DATABASE", "/data/yunpin-sync.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		log.Fatalf("prepare data directory: %v", err)
	}

	application, err := server.New(context.Background(), databasePath, os.Stdout)
	if err != nil {
		log.Fatalf("initialize server: %v", err)
	}
	defer application.Close()

	httpServer := &http.Server{
		Addr:              listenAddress,
		Handler:           application,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	shutdownContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownContext.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	log.Printf("yunpin-sync listening on %s", listenAddress)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
