package main

import (
	"os"

	"sandx/internal/cli"
)

// version 由 make release 用 -ldflags 注入（git describe）；源码构建时是 dev。
var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Execute())
}
