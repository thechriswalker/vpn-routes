package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vpn-routes/internal/config"
	"vpn-routes/internal/devicewatch"
	"vpn-routes/internal/reconcile"
	"vpn-routes/internal/state"
)

const deviceProbeFrequency = 1 * time.Second

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		// No logger yet; keep it simple.
		_, _ = os.Stderr.WriteString("vpn-routes config error: " + err.Error() + "\n")
		os.Exit(2)
	}

	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	st, err := state.NewFileStore(cfg.StatePath)
	if err != nil {
		logger.Error("state init error", "err", err)
		os.Exit(1)
	}

	logger.Info("starting",
		"dev", cfg.Dev,
		"freq", cfg.Frequency.String(),
		"dry_run", cfg.DryRun,
		"state_path", cfg.StatePath,
		"hosts_count", len(cfg.HostsRaw),
		"log_level", cfg.LogLevel,
		"show", cfg.Show,
	)
	r := reconcile.New(cfg, st, logger)

	if cfg.Show {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.Show(ctx); err != nil {
			logger.Error("show error", "err", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reconcileTicker := time.NewTicker(cfg.Frequency)
	defer reconcileTicker.Stop()

	devEvents := devicewatch.Start(ctx, cfg.Dev, deviceProbeFrequency, logger)

	devPresent := false

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := r.Cleanup(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("cleanup error", "err", err)
			}
			logger.Info("exiting", "reason", ctx.Err())
			return
		case ev := <-devEvents:
			devPresent = ev.Present
			switch ev.Type {
			case devicewatch.EventInitial:
				logger.Debug("device state detected", "dev", ev.Dev, "present", ev.Present)
				if ev.Present {
					if err := r.Tick(ctx); err != nil {
						logger.Error("initial tick error", "err", err)
					}
				}
			case devicewatch.EventAppeared:
				logger.Info("interface detected; reconciling now", "dev", ev.Dev)
				if err := r.Tick(ctx); err != nil {
					logger.Error("transition tick error", "err", err)
				}
			case devicewatch.EventDisappeared:
				logger.Info("interface disappeared; waiting for return", "dev", ev.Dev)
			}
		case <-reconcileTicker.C:
			if !devPresent {
				logger.Debug("skip periodic reconcile: interface missing", "dev", cfg.Dev)
				continue
			}
			if err := r.Tick(ctx); err != nil {
				logger.Error("tick error", "err", err)
			}
		}
	}
}
