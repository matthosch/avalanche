// Command payments-mock serves a Stripe-compatible subset of the PaymentIntents API for the
// stripe-go SDK to talk to (ADR-0007).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/paymentsmock"
)

func main() {
	if err := run(); err != nil {
		slog.Error("payments-mock stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := config.Env("PAYMENTS_MOCK_ADDR", ":12111")
	srv := &http.Server{Addr: addr, Handler: paymentsmock.NewHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("payments-mock listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
