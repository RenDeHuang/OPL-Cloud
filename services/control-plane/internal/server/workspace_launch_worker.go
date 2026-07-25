package server

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

const defaultWorkspaceLaunchInterval = 10 * time.Second

func workspaceLaunchWorkerEnabled() bool {
	value := strings.TrimSpace(os.Getenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func workspaceLaunchWorkerInterval() time.Duration {
	return durationFromEnv("OPL_WORKSPACE_LAUNCH_INTERVAL_MS", defaultWorkspaceLaunchInterval)
}

func (app *controlPlaneServer) startWorkspaceLaunchWorker(ctx context.Context, service *controlplane.Service, interval time.Duration) {
	if interval <= 0 {
		interval = defaultWorkspaceLaunchInterval
	}
	go func() {
		if err := app.runWorkspaceLaunchesOnce(ctx, service); err != nil {
			log.Printf("workspace launch failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := app.runWorkspaceLaunchesOnce(ctx, service); err != nil {
					log.Printf("workspace launch failed: %v", err)
				}
			}
		}
	}()
}
