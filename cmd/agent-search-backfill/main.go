// Package main implements the one-time backfill CLI for agent_message_search.
//
// Usage:
//
//	go run ./cmd/agent-search-backfill --config config_dev.yaml --batch-size 500
//
// After this task ships, run this CLI once per environment to populate
// agent_message_search with rows extracted from existing agent_run.messages.
// Idempotent — re-running only inserts new UUIDs (diff by message_uuid).
//
// Failure-tolerant: per-run errors are logged warn and skipped; the loop
// continues so a single bad row doesn't halt the backfill.
//
// agent-mode-v15-memory-layer-a Task 3.5.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/viper"

	"numind-server/internal/numind/biz/agent/search"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/pkg/db"
)

func main() {
	var (
		configPath string
		batchSize  int
	)
	flag.StringVar(&configPath, "config", "", "path to config_*.yaml (defaults to viper search path)")
	flag.IntVar(&batchSize, "batch-size", 500, "agent_run rows per scan batch (default 500)")
	flag.Parse()

	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		viper.SetConfigName("config_dev")
		viper.AddConfigPath(".")
		viper.AddConfigPath("./numind-server")
	}
	if err := viper.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		os.Exit(1)
	}

	// Init logger so log.Infow / log.Warnw write to stdout for ops visibility.
	log.Init(&log.Options{
		Level:       viper.GetString("log.level"),
		Format:      viper.GetString("log.format"),
		OutputPaths: viper.GetStringSlice("log.output-paths"),
	})
	defer log.Sync()

	dbOptions := &db.MySQLOptions{
		Host:                  viper.GetString("db.host"),
		Username:              viper.GetString("db.username"),
		Password:              viper.GetString("db.password"),
		Database:              viper.GetString("db.database"),
		MaxIdleConnections:    viper.GetInt("db.max-idle-connections"),
		MaxOpenConnections:    viper.GetInt("db.max-open-connections"),
		MaxConnectionLifeTime: viper.GetDuration("db.max-connection-life-time"),
		LogLevel:              viper.GetInt("db.log-level"),
	}
	gormDB, err := db.NewMySQL(dbOptions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db connect failed: %v\n", err)
		os.Exit(1)
	}
	searchStore := store.NewAgentMessageSearchStore(gormDB)

	// Honour SIGINT / SIGTERM so users can ctrl-C mid-backfill cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Warnw("agent-search-backfill: signal received, cancelling")
		cancel()
	}()

	log.Infow("agent-search-backfill starting",
		"batch_size", batchSize,
		"db_host", viper.GetString("db.host"),
		"db_name", viper.GetString("db.database"),
	)

	result, err := search.BackfillAll(ctx, gormDB, searchStore, batchSize)
	if err != nil {
		log.Errorw("agent-search-backfill failed",
			"scanned", result.ScannedRuns,
			"inserted", result.InsertedRows,
			"skipped", result.SkippedRuns,
			"elapsed", result.Elapsed.String(),
			"error", err,
		)
		os.Exit(2)
	}
	log.Infow("agent-search-backfill complete",
		"scanned_runs", result.ScannedRuns,
		"inserted_rows", result.InsertedRows,
		"skipped_runs", result.SkippedRuns,
		"elapsed", result.Elapsed.String(),
	)
}
