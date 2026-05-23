package main

import (
	"fmt"
	"numind-server/internal/numind"
	"os"
)

func main() {
	// #region agent log
	fmt.Printf("[DEBUG] main() function entry hypothesisId=A location=main.go:9 runId=startup\n")
	// #endregion
	command := numind.NewNumindCommand()
	// agent-mode-v2-skill-as-artifact T06: 注册一次性迁移 CLI 子命令。
	// 通过 docker exec ... migrate-skill-from-agent 调用；详见 docs spec §2.1 / ADR-15。
	command.AddCommand(newMigrateSkillFromAgentCmd())
	// #region agent log
	fmt.Printf("[DEBUG] Before command.Execute() hypothesisId=A location=main.go:11 runId=startup\n")
	// #endregion
	if err := command.Execute(); err != nil {
		// #region agent log
		fmt.Printf("[DEBUG] command.Execute() error hypothesisId=A location=main.go:12 runId=startup error=%s\n", err.Error())
		// #endregion
		os.Exit(2)
	}
}
