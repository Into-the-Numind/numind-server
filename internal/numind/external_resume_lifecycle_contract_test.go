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

func TestProductionHTTPRoutesAndReclaimerShareOneBizInstance(t *testing.T) {
	routerRaw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(routerRaw), "biz.NewBiz(") {
		t.Fatal("router must use the entrypoint-owned Biz; a second Biz creates a different runner/cancel map")
	}
	for _, name := range []string{"numind.go", "server.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "installNumindRouters(g, bizLayer)") {
			t.Fatalf("%s must install HTTP routes with the same Biz that owns the reclaimer", name)
		}
	}
}
