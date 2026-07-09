package server

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultArchiveRetentionInterval = 24 * time.Hour

func archiveRetentionWorkerEnabled() bool {
	value := strings.TrimSpace(os.Getenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func archiveRetentionWorkerInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("OPL_ARCHIVE_RETENTION_INTERVAL_MS"))
	if raw == "" {
		return defaultArchiveRetentionInterval
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return defaultArchiveRetentionInterval
	}
	return time.Duration(ms) * time.Millisecond
}

func (app *controlPlaneServer) startArchiveRetentionWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultArchiveRetentionInterval
	}
	go func() {
		if err := app.runArchiveRetentionOnce(ctx); err != nil {
			log.Printf("archive retention failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := app.runArchiveRetentionOnce(ctx); err != nil {
					log.Printf("archive retention failed: %v", err)
				}
			}
		}
	}()
}

func (app *controlPlaneServer) runArchiveRetentionOnce(ctx context.Context) error {
	if _, err := app.archiveTerminalResources(ctx, map[string]any{"reason": "scheduled_terminal_retention"}); err != nil {
		return err
	}
	_, err := app.applyRetention(ctx)
	return err
}
