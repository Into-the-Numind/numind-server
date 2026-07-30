package sandbox

import (
	"errors"
	"testing"
)

func TestClientFactorySelectsBackend(t *testing.T) {
	tests := []struct {
		name        string
		backend     Backend
		wantEnabled bool
		wantDocker  int
		wantBroker  int
	}{
		{name: "disabled", backend: BackendDisabled},
		{name: "docker", backend: BackendDocker, wantEnabled: true, wantDocker: 1},
		{name: "broker", backend: BackendBroker, wantEnabled: true, wantBroker: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultSandboxConfig
			cfg.Backend = tt.backend
			cfg.PoolMin = 0
			cfg.BrokerOwnerID = "api-primary"
			dockerCalls := 0
			brokerCalls := 0
			pool, err := newPoolFromConfig(
				cfg,
				nil,
				func(Logger) DockerClient {
					dockerCalls++
					return NewMockDockerClient()
				},
				func(SandboxConfig, Logger) (DockerClient, error) {
					brokerCalls++
					return NewMockDockerClient(), nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if pool.IsEnabled() != tt.wantEnabled {
				t.Fatalf("IsEnabled = %v", pool.IsEnabled())
			}
			if dockerCalls != tt.wantDocker || brokerCalls != tt.wantBroker {
				t.Fatalf("factory calls docker=%d broker=%d", dockerCalls, brokerCalls)
			}
		})
	}
}

func TestClientFactoryBrokerConfigErrorDisablesPool(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendBroker
	brokerErr := errors.New("bad broker config")
	pool, err := newPoolFromConfig(
		cfg,
		nil,
		func(Logger) DockerClient {
			t.Fatal("docker factory called for broker backend")
			return nil
		},
		func(SandboxConfig, Logger) (DockerClient, error) {
			return nil, brokerErr
		},
	)
	if !errors.Is(err, brokerErr) {
		t.Fatalf("err = %v", err)
	}
	defer pool.Close()
	if pool.IsEnabled() {
		t.Fatal("broker factory error did not disable pool")
	}
}

func TestClientFactoryBrokerRuntimeUnavailableDoesNotBlockStartup(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendBroker
	cfg.PoolMin = 1
	cfg.PoolMaxWaitMs = 1
	cfg.BrokerOwnerID = "api-primary"
	client := NewMockDockerClient()
	client.SpawnErr = ErrBrokerUnavailable

	pool, err := newPoolFromConfig(
		cfg,
		nil,
		func(Logger) DockerClient {
			t.Fatal("docker factory called for broker backend")
			return nil
		},
		func(SandboxConfig, Logger) (DockerClient, error) {
			return client, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if !pool.IsEnabled() {
		t.Fatal("broker runtime failure disabled the configured broker pool")
	}
}

func TestClientFactoryUnknownBackendDisablesPool(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.Backend = Backend("surprise")
	pool, err := newPoolFromConfig(
		cfg,
		nil,
		func(Logger) DockerClient {
			t.Fatal("docker factory called for unknown backend")
			return nil
		},
		func(SandboxConfig, Logger) (DockerClient, error) {
			t.Fatal("broker factory called for unknown backend")
			return nil, nil
		},
	)
	if !errors.Is(err, ErrSandboxDisabled) {
		t.Fatalf("err = %v", err)
	}
	defer pool.Close()
	if pool.IsEnabled() {
		t.Fatal("unknown backend did not disable pool")
	}
}
