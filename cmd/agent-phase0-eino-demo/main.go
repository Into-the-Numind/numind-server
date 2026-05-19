// Phase 0 V2 verification: Eino + aiservice integration demo.
//
// This demo validates assumption A2: that cloudwego/eino can be wrapped via
// AiserviceAdapter to call LLMs through numind-server's aiservice.Chat(), while
// preserving Langfuse tracing, billing (Reserve/Reconcile), and route fallback.
//
// # Usage
//
//	# Happy path (run from the numind-server repo root with config_local.yaml present)
//	go run ./cmd/agent-phase0-eino-demo/
//
//	# Error path — exercises the error-generation Langfuse recording path
//	go run ./cmd/agent-phase0-eino-demo/ --error-path
//
// For S5 acceptance SQL, see README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/spf13/viper"
	"gorm.io/gorm"

	aiservicemw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/langfuse"
	"numind-server/pkg/db"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/adapter"
)

func main() {
	// Recover any unexpected panic so error path never crashes without log.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[phase0-eino-demo] PANIC recovered: %v\n", r)
			os.Exit(1)
		}
	}()

	errorPath := flag.Bool("error-path", false, "Run the error path demo (non-existent model)")
	cfgFile := flag.String("config", "", "Path to config file (default: auto-discover config_local.yaml)")
	flag.Parse()

	// ── 1. Load config ──────────────────────────────────────────────────────────
	if err := loadConfig(*cfgFile); err != nil {
		log.Fatalf("[phase0-eino-demo] Failed to load config: %v", err)
	}

	// ── 2. Init Langfuse ─────────────────────────────────────────────────────────
	langfuse.Init(langfuse.LoadConfig())

	// ── 3. Init DB + aiservice Gateway ──────────────────────────────────────────
	dbInst, err := initDB()
	if err != nil {
		log.Fatalf("[phase0-eino-demo] Failed to connect to DB: %v", err)
	}
	if err := initAiservice(dbInst); err != nil {
		log.Fatalf("[phase0-eino-demo] Failed to init aiservice gateway: %v", err)
	}

	// ── 4. Run the demo ─────────────────────────────────────────────────────────
	if *errorPath {
		runErrorPathDemo()
		os.Exit(1) // error path always exits non-zero to signal expected failure
	}
	runHappyPathDemo()
}

// runHappyPathDemo runs a single ReAct loop with the get_current_date tool.
func runHappyPathDemo() {
	ctx := context.Background()

	// Create Langfuse trace for this demo run.
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "phase0-eino-demo-trace",
		langfuse.WithUserID(0),
		langfuse.WithTraceInput(map[string]any{
			"user_question":   "今天是星期几？",
			"max_react_steps": 5,
			"demo_run":        true,
		}),
		langfuse.WithTraceTags("phase0", "phase0-verification"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	agent, err := buildAgent(ctx, "")
	if err != nil {
		log.Fatalf("[phase0-eino-demo] Failed to build ReAct agent: %v", err)
	}

	fmt.Println("[phase0-eino-demo] Running happy path: 今天是星期几？")
	resp, err := agent.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "今天是星期几？"},
	})
	if err != nil {
		log.Fatalf("[phase0-eino-demo] agent.Generate error: %v", err)
	}

	fmt.Printf("[phase0-eino-demo] Final answer: %s\n", resp.Content)
	fmt.Printf("[phase0-eino-demo] Trace ID: %s (check Langfuse backend for generation + span)\n", traceID)

	// Flush Langfuse events before exit.
	flushLangfuse()
}

// runErrorPathDemo runs with a non-existent model to exercise the error code path.
// Expected: aiservice.Chat returns error, we log to stderr; Langfuse records an error generation.
func runErrorPathDemo() {
	ctx := context.Background()

	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "phase0-eino-demo-error-trace",
		langfuse.WithUserID(0),
		langfuse.WithTraceInput(map[string]any{
			"user_question": "今天是星期几？",
			"error_path":    true,
		}),
		langfuse.WithTraceTags("phase0", "phase0-verification", "error-path"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// non-existent-model-xyz will not match any route in the DB registry
	agent, err := buildAgent(ctx, "non-existent-model-xyz")
	if err != nil {
		// Record error generation before exiting.
		tc := langfuse.FromContext(ctx)
		if tc != nil {
			genID := langfuse.SpanID()
			langfuse.CreateGeneration(tc.TraceID, genID,
				langfuse.WithGenName("phase0-error-path-demo"),
				langfuse.WithGenOutput(map[string]string{"error": err.Error()}),
			)
			langfuse.EndGeneration(tc.TraceID, genID)
		}
		fmt.Fprintf(os.Stderr, "[phase0-eino-demo] Error path: build agent error (expected): %v\n", err)
		flushLangfuse()
		return
	}

	_, err = agent.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "今天是星期几？"},
	})
	if err != nil {
		// Record error generation to Langfuse.
		tc := langfuse.FromContext(ctx)
		if tc != nil {
			genID := langfuse.SpanID()
			langfuse.CreateGeneration(tc.TraceID, genID,
				langfuse.WithGenName("phase0-error-path-demo"),
				langfuse.WithGenOutput(map[string]string{"error": err.Error()}),
			)
			langfuse.EndGeneration(tc.TraceID, genID)
		}
		fmt.Fprintf(os.Stderr, "[phase0-eino-demo] Error path: got expected error: %v\n", err)
		fmt.Fprintf(os.Stderr, "[phase0-eino-demo] Error path: Trace ID %s — check Langfuse for error generation\n", traceID)
	} else {
		fmt.Fprintf(os.Stderr, "[phase0-eino-demo] Error path: expected an error but got none — verify non-existent model rejection in DB registry\n")
	}
	flushLangfuse()
}

// buildAgent constructs the Eino ReAct agent with the aiservice adapter.
// modelName may be empty (uses task profile default) or a specific override.
func buildAgent(ctx context.Context, modelName string) (*react.Agent, error) {
	chatAdapter := &AiserviceAdapter{modelName: modelName}
	dateTool := newGetCurrentDateTool()

	return react.NewAgent(ctx, &react.AgentConfig{
		Model: chatAdapter, // deprecated field but still functional in v0.8.13
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{dateTool},
		},
		MaxStep: 5,
	})
}

// ── Bootstrap helpers ────────────────────────────────────────────────────────

// loadConfig discovers and loads the numind-server configuration file.
// Priority: --config flag > config_local.yaml in CWD or repo root.
func loadConfig(cfgFile string) error {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
		return viper.ReadInConfig()
	}

	// Walk up from CWD to find config_local.yaml (demo may be run from any dir).
	candidates := []string{
		"config_local.yaml",
		"../../config_local.yaml",
		"../../../config_local.yaml",
		filepath.Join(os.Getenv("HOME"), "numind-server", "config_local.yaml"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			viper.SetConfigFile(c)
			return viper.ReadInConfig()
		}
	}

	return errors.New("could not find config_local.yaml; use --config to specify the path")
}

// initDB creates and returns a *gorm.DB using config values loaded by viper.
func initDB() (*gorm.DB, error) {
	opts := &db.MySQLOptions{
		Host:                  viper.GetString("db.host"),
		Username:              viper.GetString("db.username"),
		Password:              viper.GetString("db.password"),
		Database:              viper.GetString("db.database"),
		MaxIdleConnections:    viper.GetInt("db.max-idle-connections"),
		MaxOpenConnections:    viper.GetInt("db.max-open-connections"),
		MaxConnectionLifeTime: viper.GetDuration("db.max-connection-life-time"),
		LogLevel:              viper.GetInt("db.log-level"),
	}
	return db.NewMySQL(opts)
}

// initAiservice builds the aiservice Gateway with the real DB-backed registry
// and all provider adapters, then installs it as the process-wide default.
// This mirrors what numind.go does, without the HTTP server or billing middleware.
func initAiservice(dbInst *gorm.DB) error {
	reg := registry.New(dbInst)
	gw := aiservice.Build(aiservice.Deps{Registry: reg})

	// Wire a minimal middleware chain (no billing for demo — Reserve/Reconcile
	// requires a CreditService which in turn needs the full store layer).
	// For full billing validation (S5 SQL checks), run the demo with a complete
	// numind-server deployment or wire aiservice through the real middleware chain.
	//
	// For Phase 0 A2 verification we only need to prove the adapter does NOT
	// break the Langfuse / route / provider path. Billing can be verified
	// separately by running the real server and triggering the same task profile.
	chain := aiservicemw.BuildDefault(aiservicemw.Deps{
		Langfuse: langfuse.C,
		Resolver: reg,
	})
	gw.SetMiddlewareChain(aiservicemw.AsGatewayChain(chain))

	// Register provider adapters.
	for _, p := range []aiservice.Provider{
		adapter.NewAliAdapter(),
		adapter.NewVolcAdapter(),
		adapter.NewDMXAPIAdapter(),
	} {
		gw.RegisterProvider(p)
	}

	aiservice.SetDefault(gw)
	return nil
}

// flushLangfuse waits briefly to let the async Langfuse client batch-send events
// before the process exits. The demo is short-lived so we cannot rely on the
// normal flush-at-shutdown path that long-running servers have.
func flushLangfuse() {
	// The Langfuse client batches events on a 3-second interval. Wait slightly
	// longer to ensure the last batch is dispatched.
	time.Sleep(4 * time.Second)
}
