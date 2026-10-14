// Command th is the ThailandHosting command line.
package main

import (
	"os"

	"github.com/mrzack99s/thailandhosting-cli/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.NewApp(version).Run(os.Args[1:]))
}
