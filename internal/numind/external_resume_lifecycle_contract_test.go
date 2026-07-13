package numind

import (
	"os"
	"strings"
	"testing"
)

func TestProductionEntrypointsStartAndStopExternalResumeReclaimer(t *testing.T) {
	for _, name := range []string{"numind.go", "server.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		start := strings.Index(source, "StartExternalResumeReclaimer()")
		stop := strings.Index(source, "CloseExternalResumeReclaimer(ctx)")
		if start < 0 || stop < 0 || stop <= start {
			t.Fatalf("%s must start the fully-wired reclaimer and stop it during shutdown", name)
		}
	}
}
