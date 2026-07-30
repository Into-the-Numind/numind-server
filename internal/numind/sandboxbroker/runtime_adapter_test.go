package sandboxbroker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestDockerRuntimeAdapterRejectsHostDockerSocket(t *testing.T) {
	policy := testRuntimePolicy(t)
	for _, host := range []string{
		"unix:///var/run/docker.sock",
		"unix:///run/docker.sock",
		"unix:///tmp/docker.sock",
		"tcp://127.0.0.1:2375",
	} {
		t.Run(host, func(t *testing.T) {
			_, err := NewDockerRuntimeAdapter(DockerRuntimeAdapterConfig{
				Policy:          policy,
				BrokerInstance:  "broker-primary",
				DockerHost:      host,
				DockerConfigDir: "/opt/numind-sandbox/docker-config",
			})
			if !errors.Is(err, ErrRuntimePolicyDenied) {
				t.Fatalf("NewDockerRuntimeAdapter err = %v", err)
			}
		})
	}
}

func TestDockerRuntimeAdapterExecUsesFixedCommandShape(t *testing.T) {
	var gotBinary string
	var gotArgs []string
	adapter := &DockerRuntimeAdapter{
		policy: testRuntimePolicy(t),
		binary: "docker",
		run: func(
			_ context.Context,
			binary string,
			args []string,
			stdin io.Reader,
			stdout io.Writer,
			_ io.Writer,
		) error {
			gotBinary = binary
			gotArgs = append([]string(nil), args...)
			if stdin != nil {
				t.Fatal("Exec passed unexpected stdin")
			}
			_, _ = stdout.Write([]byte("ok"))
			return nil
		},
	}

	response, err := adapter.Exec(
		context.Background(),
		"container-123",
		[]string{"python", "-c", "print('ok')"},
		[]string{"NUMIND_OUTPUT_FORMAT=json"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.Stdout != "ok" {
		t.Fatalf("stdout = %q", response.Stdout)
	}
	if gotBinary != "docker" {
		t.Fatalf("binary = %q", gotBinary)
	}
	want := []string{
		"exec",
		"--user=1000:1000",
		"--workdir=/workdir",
		"--env=NUMIND_OUTPUT_FORMAT=json",
		"container-123",
		"python",
		"-c",
		"print('ok')",
	}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("docker args = %#v; want %#v", gotArgs, want)
	}
	joined := strings.Join(gotArgs, " ")
	for _, forbidden := range []string{
		"--privileged",
		"--volume",
		"--mount",
		"/var/run/docker.sock",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("exec args contain forbidden %q: %s", forbidden, joined)
		}
	}
}

func TestDockerRuntimeAdapterListParsesBrokerInstance(t *testing.T) {
	adapter := &DockerRuntimeAdapter{
		policy: testRuntimePolicy(t),
		binary: "docker",
		output: func(context.Context, string, []string) ([]byte, error) {
			return []byte("container-1\tlease-1\tbroker-primary\n"), nil
		},
	}
	containers, err := adapter.ListSandboxContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 ||
		containers[0].ContainerID != "container-1" ||
		containers[0].LeaseID != "lease-1" ||
		containers[0].BrokerInstance != "broker-primary" {
		t.Fatalf("containers = %#v", containers)
	}

	adapter.output = func(context.Context, string, []string) ([]byte, error) {
		return []byte("container-1\tlease-1\n"), nil
	}
	if _, err := adapter.ListSandboxContainers(context.Background()); !errors.Is(
		err,
		ErrRPCProtocol,
	) {
		t.Fatalf("malformed list err = %v", err)
	}
}

func TestDockerRuntimeAdapterCopyOutRejectsSymlinkTar(t *testing.T) {
	payload := runtimeAdapterTar(t, &tar.Header{
		Name:     "output/link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
	})
	adapter := &DockerRuntimeAdapter{
		policy: testRuntimePolicy(t),
		binary: "docker",
		stream: func(
			context.Context,
			string,
			[]string,
		) (io.ReadCloser, func() error, error) {
			return io.NopCloser(bytes.NewReader(payload)), func() error {
				return nil
			}, nil
		},
	}
	_, err := adapter.CopyOut(
		context.Background(),
		"container-123",
		CopyOutSource{Root: "/workdir", Relative: "output"},
	)
	if !errors.Is(err, ErrStreamPolicyDenied) {
		t.Fatalf("CopyOut symlink err = %v", err)
	}
}

func runtimeAdapterTar(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
