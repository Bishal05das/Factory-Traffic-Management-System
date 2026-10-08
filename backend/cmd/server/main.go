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

	"factorytraffic/internal/adapters/controller"
	httpadapter "factorytraffic/internal/adapters/http"
	"factorytraffic/internal/adapters/postgres"
	"factorytraffic/internal/application"
)

func main() {
	if err := run(); err != nil {
		slog.Error("backend stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	store, err := postgres.Open(startup, dsn)
	if err != nil {
		cancel()
		return err
	}
	defer store.DB.Close()
	if err = store.Migrate(startup); err != nil {
		cancel()
		return err
	}
	owner, err := store.AcquireOwnership(startup)
	if err != nil {
		cancel()
		return err
	}
	defer owner.Close()
	app := application.New(store, log)
	if err = app.SeedAndRecover(startup); err != nil {
		cancel()
		return err
	}
	cancel()
	server := &http.Server{Addr: address, Handler: (httpadapter.Server{App: app, Controller: controller.REST{Store: store}, Log: log}).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	failures := make(chan error, 3)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- err
		}
	}()
	go func() {
		if err := app.RunScheduler(ctx); err != nil && !errors.Is(err, context.Canceled) {
			failures <- err
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, done := context.WithTimeout(ctx, time.Second)
				err := owner.PingContext(check)
				done()
				if err != nil {
					failures <- err
					return
				}
			}
		}
	}()
	log.Info("backend ready", "address", address, "version", os.Getenv("APP_VERSION"))
	select {
	case <-ctx.Done():
	case err = <-failures:
		log.Error("control continuity lost; shutting down", "error", err)
		stop()
	}
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = server.Shutdown(shutdown)
	return err
}
