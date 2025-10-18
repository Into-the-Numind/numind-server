package main

import (
	"numind-server/internal/numind"
	"os"
)

func main() {
	command := numind.NewNumindCommand()
	if err := command.Execute(); err != nil {
		os.Exit(2)
	}
}
