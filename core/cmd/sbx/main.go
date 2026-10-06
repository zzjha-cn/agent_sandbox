package main

import (
	"os"

	"sandx/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
