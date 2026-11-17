package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/swarn007-byte/Relay/internal/api"
	"github.com/swarn007-byte/Relay/internal/config"
	"github.com/swarn007-byte/Relay/internal/security"
	"github.com/swarn007-byte/Relay/internal/storage"
	"github.com/swarn007-byte/Relay/internal/worker"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	db, err := storage.OpenConfigured(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()

	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = storage.CreateTables(initCtx, db)
	initCancel()
	if err != nil {
		return fmt.Errorf("schema initialization: %w", err)
	}
	log.Println("Database initialized")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	guard := security.NewGuard(cfg.AllowPrivateDestinations, cfg.AllowedPorts)
	client := worker.NewClient(cfg.HTTPTimeout, guard, cfg.WebhookSecret)
	jobs := make(chan storage.DeliveryJob, cfg.WorkerCount*2)

	poolWG := worker.StartPool(ctx, db, jobs, cfg.WorkerCount, client, worker.Options{
		MaxAttempts:    cfg.MaxAttempts,
		RetryBaseDelay: cfg.RetryBaseDelay,
		RetryMaxDelay:  cfg.RetryMaxDelay,
		LeaseTTL:       cfg.LeaseTTL,
	})

	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		worker.RunScheduler(ctx, db, jobs, worker.SchedulerOptions{
			Interval:  cfg.SchedulerInterval,
			BatchSize: cfg.BatchSize,
			LeaseTTL:  cfg.LeaseTTL,
		})
	}()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.New(&cfg, db, guard).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Relay server running on port %s", cfg.Port)
		serverErr <- srv.ListenAndServe()
	}()

	var runErr error
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		log.Println("Shutdown signal received")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Println("HTTP shutdown error:", err)
	}

	stop()
	<-schedulerDone
	close(jobs)

	waitForWorkers(poolWG, cfg.ShutdownTimeout)

	log.Println("Shutdown complete")
	return runErr
}

func waitForWorkers(wg *sync.WaitGroup, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		log.Println("Shutdown deadline exceeded while waiting for workers")
	}
}
