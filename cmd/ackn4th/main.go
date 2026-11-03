// Command ackn4th orchestrates teams of AI agents working on code.
package main

import (
	"context"
	"os"

	"github.com/dethmasque/ackn4th/internal/cli"
	"github.com/dethmasque/ackn4th/internal/voice"
)

func main() {
	// The dashboard's voice runs as a helper process (see voice.Serve).
	if len(os.Args) > 1 && os.Args[1] == "__voice" {
		os.Exit(voice.Main(os.Args[2:]))
	}
	os.Exit(cli.Execute(context.Background()))
}
