package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/config"
	"github.com/brunobrsr1/fraud-scoring-platform/internal/httpapi"
	"github.com/brunobrsr1/fraud-scoring-platform/internal/model"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 30 * time.Second
)

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags)

	cfg, err := config.Load()
	if err != nil {
		logger.Printf("configuration error: %v", err)
		os.Exit(1)
	}

	logger.Printf("loading model: %s", cfg.ModelPath)

	model, err := model.Load(cfg.ModelPath)
	if err != nil {
		logger.Printf("model loading error: %v", err)
		os.Exit(1)
	}

	logger.Printf("loaded model: %s", model.Version)

	handler := httpapi.New()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		logger.Printf("listen error: %v", err)
		os.Exit(1)
	}

	logger.Printf("listening on %s", cfg.HTTPAddr)

	serverErr := make(chan error, 1)

	go func() {
		serverErr <- srv.Serve(ln)
	}()

	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	select {
	case <-ctx.Done():
		logger.Printf("shutdown signal received")

		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			shutdownTimeout,
		)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Printf("graceful shutdown failed: %v", err)

			if closeErr := srv.Close(); closeErr != nil {
				logger.Printf("forced close failed: %v", closeErr)
			}
			os.Exit(1)
		}
		logger.Printf("server stopped cleanly")

	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("server error: %v", err)
			os.Exit(1)
		}
	}
}
