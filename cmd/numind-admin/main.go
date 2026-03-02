package main

import (
	"numind-server/internal/numind"
	"os"
)

func main() {
	command := numind.NewAdminCommand()
	if err := command.Execute(); err != nil {
		os.Exit(2)
	}
}
