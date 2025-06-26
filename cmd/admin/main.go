package numindadmin

import (
	"os"

	"numind-server/internal/numind"
)

func main() {
	command := numind.NewNumindCommand()
	if err := command.Execute(); err != nil {
		os.Exit(2)
	}
}
