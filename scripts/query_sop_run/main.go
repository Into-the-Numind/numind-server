package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"numind-server/pkg/db"

	"github.com/spf13/viper"
	"gorm.io/gorm"
)

type sopRunRow struct {
	ID             uint
	Status         string
	ConversationID string
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ErrorMessage   string
	FinalNoteID    *uint
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type sopNodeRunRow struct {
	ID               uint
	RunID            uint
	NodeID           uint
	Status           string
	Output           string
	Thinking         string
	ErrorMessage     string
	LatencyMS        int64
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	ReasoningTokens  int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func main() {
	runID := uint(66)

	gormDB, loadedConfig := mustConnectDB()

	var run sopRunRow
	if err := gormDB.Table("sop_run").Where("id = ?", runID).Take(&run).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			fmt.Printf("run_id=%d not found in %s\n", runID, loadedConfig)
			return
		}
		log.Fatalf("query sop_run failed: %v", err)
	}

	fmt.Printf("SOP Run (run_id=%d)\n", runID)
	fmt.Printf("  status=%s conversation_id=%s final_note_id=%v\n", run.Status, run.ConversationID, run.FinalNoteID)
	fmt.Printf("  started_at=%s finished_at=%s\n", formatTime(run.StartedAt), formatTime(run.FinishedAt))
	fmt.Printf("  error_message=%s\n", oneLine(run.ErrorMessage))
	fmt.Printf("  created_at=%s updated_at=%s\n", run.CreatedAt.Format(time.RFC3339), run.UpdatedAt.Format(time.RFC3339))

	var nodeRuns []sopNodeRunRow
	if err := gormDB.Table("sop_node_run").Where("run_id = ?", runID).Find(&nodeRuns).Error; err != nil {
		log.Fatalf("query sop_node_run failed: %v", err)
	}

	sort.Slice(nodeRuns, func(i, j int) bool { return nodeRuns[i].ID < nodeRuns[j].ID })
	fmt.Printf("\nNode Runs (%d)\n", len(nodeRuns))
	for _, nr := range nodeRuns {
		fmt.Printf("- node_run_id=%d node_id=%d status=%s latency_ms=%d\n", nr.ID, nr.NodeID, nr.Status, nr.LatencyMS)
		fmt.Printf("  error=%s\n", oneLine(nr.ErrorMessage))
		fmt.Printf("  output_len=%d thinking_len=%d tokens(p=%d c=%d t=%d r=%d)\n",
			len(nr.Output), len(nr.Thinking), nr.PromptTokens, nr.CompletionTokens, nr.TotalTokens, nr.ReasoningTokens)
		fmt.Printf("  output_tail=%q\n", tail(nr.Output, 240))
	}
}

func mustConnectDB() (*gorm.DB, string) {
	configFiles := []string{"config_qa", "config_dev", "config_prod", "config_local"}
	for _, configName := range configFiles {
		viper.Reset()
		viper.SetConfigName(configName)
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("./configs")

		if err := viper.ReadInConfig(); err != nil {
			continue
		}

		opts := &db.MySQLOptions{
			Host:                  viper.GetString("db.host"),
			Username:              viper.GetString("db.username"),
			Password:              viper.GetString("db.password"),
			Database:              viper.GetString("db.database"),
			MaxIdleConnections:    viper.GetInt("db.max-idle-connections"),
			MaxOpenConnections:    viper.GetInt("db.max-open-connections"),
			MaxConnectionLifeTime: viper.GetDuration("db.max-connection-life-time"),
			LogLevel:              0,
		}

		gormDB, err := db.NewMySQL(opts)
		if err != nil {
			continue
		}
		sqlDB, err := gormDB.DB()
		if err != nil {
			continue
		}
		if err := sqlDB.Ping(); err != nil {
			continue
		}

		fmt.Printf("✓ Connected DB via %s (%s/%s)\n", configName, opts.Host, opts.Database)
		return gormDB, configName
	}
	log.Fatalf("failed to connect DB using any config")
	return nil, ""
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "(nil)"
	}
	return t.Format(time.RFC3339)
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

func tail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[len(r)-n:])
}
