package sandbox

import "fmt"

// DockerClientFactory creates the dev-only direct Docker client.
type DockerClientFactory func(Logger) DockerClient

// BrokerClientFactory creates the production broker client.
type BrokerClientFactory func(SandboxConfig, Logger) (DockerClient, error)

// NewPoolFromConfig builds the sandbox Pool for the configured backend.
// The production-safe fallback is always disabled: invalid broker config or an
// unknown backend must not stop the user API from booting.
func NewPoolFromConfig(cfg SandboxConfig, logger Logger) (Pool, error) {
	return newPoolFromConfig(
		cfg,
		logger,
		NewDockerCLIClient,
		NewBrokerDockerClient,
	)
}

func newPoolFromConfig(
	cfg SandboxConfig,
	logger Logger,
	newDocker DockerClientFactory,
	newBroker BrokerClientFactory,
) (Pool, error) {
	switch cfg.Backend {
	case BackendDisabled, "":
		disabled := cfg
		disabled.Backend = BackendDisabled
		return NewPool(disabled, nil, logger), nil
	case BackendDocker:
		if newDocker == nil {
			return NewPool(disabledSandboxConfig(cfg), nil, logger),
				fmt.Errorf("%w: docker client factory missing", ErrSandboxDisabled)
		}
		return NewPool(cfg, newDocker(logger), logger), nil
	case BackendBroker:
		if newBroker == nil {
			return NewPool(disabledSandboxConfig(cfg), nil, logger),
				fmt.Errorf("%w: broker client factory missing", ErrSandboxDisabled)
		}
		client, err := newBroker(cfg, logger)
		if err != nil {
			return NewPool(disabledSandboxConfig(cfg), nil, logger), err
		}
		return NewPool(cfg, client, logger), nil
	default:
		return NewPool(disabledSandboxConfig(cfg), nil, logger),
			fmt.Errorf("%w: unknown backend %q", ErrSandboxDisabled, cfg.Backend)
	}
}

func disabledSandboxConfig(cfg SandboxConfig) SandboxConfig {
	cfg.Backend = BackendDisabled
	return cfg
}
