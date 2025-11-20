package main

import (
	numindadmin "numind-server/internal/numind-admin"
	"os"
)

func main() {
	command := numindadmin.NewNumindAdminCommand()
	if err := command.Execute(); err != nil {
		os.Exit(2)
	}
}
