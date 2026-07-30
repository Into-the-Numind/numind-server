package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/viper"

	creditbiz "numind-server/internal/numind/biz/credit"
	membershipbiz "numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/sandboxreconcile"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/pkg/db"
)

type options struct {
	apply        bool
	brokerSocket string
	configPath   string
	limit        int
}

type serviceRunner interface {
	Run(context.Context) (sandboxreconcile.Result, error)
}

type runtimeFactory func(context.Context, options, io.Writer) (serviceRunner, func(), error)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	return runWithFactory(args, stdout, stderr, newRuntimeService)
}

func runWithFactory(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	factory runtimeFactory,
) int {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	mode := "dry-run"
	if opts.apply {
		mode = "apply"
	}
	_, _ = fmt.Fprintf(
		stdout,
		"numind-sandbox-reconcile mode=%s broker_socket=%s config=%s limit=%d\n",
		mode,
		opts.brokerSocket,
		opts.configPath,
		opts.limit,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner, cleanup, err := factory(ctx, opts, stderr)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "setup failed: %v\n", err)
		return 1
	}
	result, err := runner.Run(ctx)
	_, _ = fmt.Fprintf(
		stdout,
		"scanned=%d would_apply=%d applied=%d skipped=%d failed=%d\n",
		result.Scanned,
		result.WouldApply,
		result.Applied,
		result.Skipped,
		result.Failed,
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reconcile failed: %v\n", err)
		return 1
	}
	return 0
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("numind-sandbox-reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(
		&opts.apply,
		"apply",
		false,
		"apply idempotent reconciliation; default is dry-run",
	)
	flags.StringVar(
		&opts.brokerSocket,
		"broker-socket",
		"/run/numind-sandbox/sandboxd.sock",
		"broker Unix socket path",
	)
	flags.StringVar(
		&opts.configPath,
		"config",
		"",
		"path to config_*.yaml; required to avoid connecting to the wrong DB",
	)
	flags.IntVar(
		&opts.limit,
		"limit",
		sandboxreconcile.DefaultLimit,
		"maximum records to scan",
	)
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if opts.limit <= 0 || opts.limit > sandboxreconcile.MaxLimit {
		return options{}, fmt.Errorf("limit must be between 1 and %d", sandboxreconcile.MaxLimit)
	}
	if opts.brokerSocket == "" {
		return options{}, fmt.Errorf("broker socket is required")
	}
	if opts.configPath == "" {
		return options{}, fmt.Errorf("config is required")
	}
	return opts, nil
}

func newRuntimeService(
	_ context.Context,
	opts options,
	stderr io.Writer,
) (serviceRunner, func(), error) {
	v := viper.New()
	v.SetConfigFile(opts.configPath)
	if err := v.ReadInConfig(); err != nil {
		return nil, nil, fmt.Errorf("config load failed: %w", err)
	}
	log.Init(&log.Options{
		Level:       v.GetString("log.level"),
		Format:      v.GetString("log.format"),
		OutputPaths: v.GetStringSlice("log.output-paths"),
	})
	dbOptions := &db.MySQLOptions{
		Host:                  v.GetString("db.host"),
		Username:              v.GetString("db.username"),
		Password:              v.GetString("db.password"),
		Database:              v.GetString("db.database"),
		MaxIdleConnections:    v.GetInt("db.max-idle-connections"),
		MaxOpenConnections:    v.GetInt("db.max-open-connections"),
		MaxConnectionLifeTime: v.GetDuration("db.max-connection-life-time"),
		LogLevel:              v.GetInt("db.log-level"),
	}
	gormDB, err := db.NewMySQL(dbOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("db connect failed: %w", err)
	}
	cleanup := func() {
		sqlDB, dbErr := gormDB.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		log.Sync()
	}
	broker, err := sandboxreconcile.NewBrokerUnixClient(opts.brokerSocket)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	ds := store.NewStore(gormDB)
	membershipSvc := membershipbiz.NewMembershipService(gormDB)
	creditSvc := creditbiz.NewCreditService(
		ds,
		creditbiz.NewCreditBiz(ds),
		nil,
		membershipSvc,
	)
	dbStore, err := sandboxreconcile.NewDBStore(gormDB, creditSvc)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	service, err := sandboxreconcile.New(sandboxreconcile.Config{
		Apply:  opts.apply,
		Limit:  opts.limit,
		Broker: broker,
		Store:  dbStore,
		Logger: sandboxreconcile.NewWriterLogger(stderr),
	})
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return service, cleanup, nil
}
