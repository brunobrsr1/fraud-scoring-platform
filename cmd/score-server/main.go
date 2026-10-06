// Command score-server runs the fraud-scoring service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/config"
	"github.com/brunobrsr1/fraud-scoring-platform/internal/httpapi"
	"github.com/brunobrsr1/fraud-scoring-platform/internal/model"
	"github.com/brunobrsr1/fraud-scoring-platform/internal/scoring"
)

// Zero means no timeout in net/http. Keep writeTimeout <= shutdownTimeout <
// docker stop's 10s grace period, or SIGKILL wins the race.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 5 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// main is the only place that exits: os.Exit would skip run's defers.
func main() {
	logger := slog.New(
		slog.NewJSONHandler(os.Stderr, nil),
	)

	if err := run(logger); err != nil {
		logger.Error("server terminated", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	// Registered first, so a SIGTERM during startup still shuts down cleanly.
	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger.Info("loading model", "path", cfg.ModelPath)

	// Without a model there is nothing to fail open to, so exit.
	m, err := model.Load(cfg.ModelPath)
	if err != nil {
		return fmt.Errorf("load model: %w", err)
	}

	logger.Info("model loaded",
		"version", m.Version,
		"features", m.FeatureCount(),
	)

	service, err := scoring.New(m, time.Now)
	if err != nil {
		return fmt.Errorf("create scoring service: %w", err)
	}

	srv := &http.Server{
		Handler:           httpapi.New(service, logger),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Bind here rather than inside Serve, so a busy port fails before we log
	// "listening".
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}

	// ln.Addr, not cfg.HTTPAddr: with ":0" the kernel picks the port.
	logger.Info("server listening",
		"addr", ln.Addr().String(),
	)

	// Buffered so the goroutine can finish even if we return via the signal
	// branch and never read it.
	serverErr := make(chan error, 1)

	go func() {
		serverErr <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received",
			"cause", context.Cause(ctx),
		)

		// ctx is already cancelled; Shutdown needs a deadline of its own.
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			shutdownTimeout,
		)
		defer cancel()

		// Shutdown waits for in-flight requests but never kills them; Close does.
		if err := srv.Shutdown(shutdownCtx); err != nil {
			if closeErr := srv.Close(); closeErr != nil {
				logger.Error("forced server close failed",
					"err", closeErr,
				)
			}

			return fmt.Errorf("shutdown: %w", err)
		}

		logger.Info("server stopped cleanly")
		return nil

	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("server failed: %w", err)
	}
}
