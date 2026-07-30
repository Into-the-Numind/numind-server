package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/numind/sandboxbroker"
)

type options struct {
	configPath string
}

type sandboxdConfig struct {
	JournalPath     string
	BrokerInstance  string
	DockerHost      string
	DockerConfigDir string
	Runtime         sandboxbroker.RuntimeConfig
	Server          sandboxbroker.ServerConfig
	AllowedAPIUIDs  []uint32
	Capacity        sandboxbroker.StaticCapacityPlanConfig
	Readiness       sandboxbroker.ReadinessConfig
}

type daemonRunner interface {
	Run(context.Context) error
}

type runtimeFactory func(context.Context, options, io.Writer) (daemonRunner, func(), error)

type brokerServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

type sandboxdRuntime struct {
	server       brokerServer
	scheduler    *sandboxbroker.Scheduler
	service      *sandboxbroker.JournalRPCService
	pressure     *sandboxbroker.PressureRunner
	drainTimeout time.Duration
}

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
	_, _ = fmt.Fprintf(
		stdout,
		"numind-sandboxd config=%s\n",
		opts.configPath,
	)
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	runner, cleanup, err := factory(ctx, opts, stderr)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "setup failed: %v\n", err)
		return 1
	}
	if err := runner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "sandboxd failed: %v\n", err)
		return 1
	}
	return 0
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("numind-sandboxd", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(
		&opts.configPath,
		"config",
		"",
		"absolute path to sandboxd-only config; config_prod.yaml is rejected",
	)
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if opts.configPath == "" || flags.NArg() != 0 {
		return options{}, fmt.Errorf("config is required")
	}
	if !filepath.IsAbs(opts.configPath) {
		return options{}, fmt.Errorf("config must be an absolute path")
	}
	return opts, nil
}

func newRuntimeService(
	ctx context.Context,
	opts options,
	_ io.Writer,
) (daemonRunner, func(), error) {
	cfg, err := loadSandboxdConfig(opts.configPath)
	if err != nil {
		return nil, nil, err
	}
	journal, err := sandboxbroker.OpenJournal(ctx, cfg.JournalPath)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		_ = journal.Close()
	}
	policy, err := sandboxbroker.NewRuntimePolicy(cfg.Runtime)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	plan, err := sandboxbroker.NewStaticCapacityPlan(cfg.Capacity)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	pressure, err := sandboxbroker.NewPressureController(plan)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	readinessSource, err := sandboxbroker.NewLinuxReadinessSource(
		sandboxbroker.LinuxReadinessSourceConfig{
			Readiness:             cfg.Readiness,
			DockerHost:            cfg.DockerHost,
			DockerConfigDir:       cfg.DockerConfigDir,
			ProcMountInfoPath:     "",
			CgroupControllersPath: "",
			FindmntBinary:         "",
			DockerBinary:          sandboxbroker.SandboxDockerBinary,
			RuntimeCommandTimeout: sandboxbroker.RuntimeExecTimeout,
		},
	)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	runtimeAdapter, err := sandboxbroker.NewDockerRuntimeAdapter(
		sandboxbroker.DockerRuntimeAdapterConfig{
			Policy:          policy,
			BrokerInstance:  cfg.BrokerInstance,
			DockerHost:      cfg.DockerHost,
			DockerConfigDir: cfg.DockerConfigDir,
		},
	)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	scheduler := sandboxbroker.NewScheduler()
	if _, err := sandboxbroker.RecoverJournalAndRuntime(
		ctx,
		sandboxbroker.RecoveryConfig{
			Journal:   journal,
			Scheduler: scheduler,
			Runtime:   runtimeAdapter,
			Timeout:   sandboxbroker.RecoveryDefaultTimeout,
		},
	); err != nil && !errors.Is(err, sandboxbroker.ErrRecoveryIncomplete) {
		cleanup()
		return nil, nil, err
	}
	pressureSource, err := sandboxbroker.NewLinuxPressureSource(
		sandboxbroker.LinuxPressureSourceConfig{
			Journal:                   journal,
			ProcMeminfoPath:           "",
			WorkloadMemoryCurrentPath: filepath.Join(cfg.Readiness.WorkloadCgroupPath, "memory.current"),
		},
	)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	readiness, err := sandboxbroker.NewReadinessChecker(
		cfg.Readiness,
		plan,
		readinessSource,
		pressure,
	)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	service, err := sandboxbroker.NewJournalRPCService(
		journal,
		scheduler,
		runtimeAdapter,
	)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	authorizer, err := sandboxbroker.NewLinuxPeerAuthorizer(cfg.AllowedAPIUIDs)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	server, err := sandboxbroker.NewServer(cfg.Server, service, authorizer)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	runner, err := sandboxbroker.NewPressureRunner(
		sandboxbroker.PressureRunnerConfig{
			Scheduler: scheduler,
			Pressure:  pressure,
			Readiness: readiness,
			Sampler:   pressureSource,
			Reclaimer: service,
		},
	)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return &sandboxdRuntime{
		server:       server,
		scheduler:    scheduler,
		service:      service,
		pressure:     runner,
		drainTimeout: sandboxbroker.SandboxDrainTimeout,
	}, cleanup, nil
}

func (r *sandboxdRuntime) Run(ctx context.Context) error {
	if r == nil || r.server == nil || r.scheduler == nil ||
		r.service == nil || r.pressure == nil {
		return sandboxbroker.ErrInvalidPressureRunnerConfig
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	pressureDone := make(chan error, 1)
	go func() {
		serverDone <- r.server.ListenAndServe()
	}()
	go func() {
		pressureDone <- r.pressure.Run(runCtx)
	}()
	select {
	case <-ctx.Done():
		cancel()
		return r.shutdown(context.Background())
	case err := <-serverDone:
		cancel()
		if err != nil {
			return err
		}
		return nil
	case err := <-pressureDone:
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			_ = r.shutdown(context.Background())
			return err
		}
		return nil
	}
}

func (r *sandboxdRuntime) shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.scheduler.SetAdmission(false, sandboxbroker.ErrSchedulerAdmissionBlocked)
	timeout := r.drainTimeout
	if timeout == 0 {
		timeout = sandboxbroker.SandboxDrainTimeout
	}
	drainCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	drainErr := sandboxbroker.DrainScheduler(
		drainCtx,
		r.scheduler,
		500*time.Millisecond,
	)
	if drainErr != nil {
		sandboxbroker.LogSandboxShutdownDrainDeadline()
		_ = r.service.MarkAllLiveRecoveryPending(
			drainCtx,
			sandboxbroker.TerminationBrokerShutdown,
		)
	}
	serverErr := r.server.Shutdown(drainCtx)
	return errors.Join(serverErr, drainErr)
}

func loadSandboxdConfig(path string) (sandboxdConfig, error) {
	if err := validateConfigFile(path); err != nil {
		return sandboxdConfig{}, err
	}
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return sandboxdConfig{}, err
	}
	settings := v.AllSettings()
	for key := range settings {
		if key != "sandboxd" {
			return sandboxdConfig{}, fmt.Errorf("sandboxd config must not include business section %q", key)
		}
	}
	if _, ok := settings["sandboxd"]; !ok {
		return sandboxdConfig{}, fmt.Errorf("sandboxd section is required")
	}
	server := sandboxbroker.DefaultServerConfig()
	if socketPath := v.GetString("sandboxd.socket.path"); socketPath != "" {
		server.SocketPath = socketPath
	}
	server.SocketUID = mustUint32(v, "sandboxd.socket.uid")
	server.SocketGID = mustUint32(v, "sandboxd.socket.gid")
	server.SocketDirectoryUID = mustUint32(v, "sandboxd.socket.dir_uid")
	server.SocketDirectoryGID = mustUint32(v, "sandboxd.socket.dir_gid")
	return sandboxdConfig{
		JournalPath:     v.GetString("sandboxd.journal_path"),
		BrokerInstance:  v.GetString("sandboxd.broker_instance"),
		DockerHost:      v.GetString("sandboxd.docker_host"),
		DockerConfigDir: v.GetString("sandboxd.docker_config_dir"),
		Runtime: sandboxbroker.RuntimeConfig{
			ImageDigest:        v.GetString("sandboxd.runtime.image_digest"),
			SeccompPath:        v.GetString("sandboxd.runtime.seccomp_path"),
			SeccompSHA256:      v.GetString("sandboxd.runtime.seccomp_sha256"),
			AllowedSkills:      v.GetStringSlice("sandboxd.runtime.allowed_skills"),
			AllowedToolEnvKeys: v.GetStringSlice("sandboxd.runtime.allowed_tool_env_keys"),
		},
		Server:         server,
		AllowedAPIUIDs: readUint32Slice(v, "sandboxd.allowed_api_uids"),
		Capacity: sandboxbroker.StaticCapacityPlanConfig{
			EvidenceMode:          sandboxbroker.CapacityEvidenceMode(v.GetString("sandboxd.capacity.evidence_mode")),
			BaselineBytes:         v.GetInt64("sandboxd.capacity.baseline_bytes"),
			ParentMaxBytes:        v.GetInt64("sandboxd.capacity.parent_max_bytes"),
			WorkloadMaxBytes:      v.GetInt64("sandboxd.capacity.workload_max_bytes"),
			WorkloadHighBytes:     v.GetInt64("sandboxd.capacity.workload_high_bytes"),
			WorkloadRecoveryBytes: v.GetInt64("sandboxd.capacity.workload_recovery_bytes"),
			WorkloadShedBytes:     v.GetInt64("sandboxd.capacity.workload_shed_bytes"),
			ControlHighBytes:      v.GetInt64("sandboxd.capacity.control_high_bytes"),
			ControlMaxBytes:       v.GetInt64("sandboxd.capacity.control_max_bytes"),
			ParentHeadroomBytes:   v.GetInt64("sandboxd.capacity.parent_headroom_bytes"),
		},
		Readiness: sandboxbroker.ReadinessConfig{
			ParentCgroupPath:   v.GetString("sandboxd.readiness.parent_cgroup_path"),
			WorkloadCgroupPath: v.GetString("sandboxd.readiness.workload_cgroup_path"),
			DataRootPath:       v.GetString("sandboxd.readiness.data_root_path"),
			DataRootUUID:       v.GetString("sandboxd.readiness.data_root_uuid"),
			ImageDigest:        v.GetString("sandboxd.runtime.image_digest"),
		},
	}, nil
}

func validateConfigFile(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("config must be an absolute clean path")
	}
	if filepath.Base(path) == "config_prod.yaml" {
		return fmt.Errorf("sandboxd must not read config_prod.yaml")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("config must be a private regular file")
	}
	return nil
}

func mustUint32(v *viper.Viper, key string) uint32 {
	value := v.GetInt64(key)
	if value < 0 || value > int64(^uint32(0)) {
		return 0
	}
	return uint32(value)
}

func readUint32Slice(v *viper.Viper, key string) []uint32 {
	values := v.GetIntSlice(key)
	result := make([]uint32, 0, len(values))
	for _, value := range values {
		if value < 0 {
			continue
		}
		result = append(result, uint32(value))
	}
	return result
}
