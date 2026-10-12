// Command plumbline-verify checks Plumbline evidence packs offline.
//
// This is the P0-1 skeleton: it prints its version. Verification arrives in P1-5.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at link time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	os.Exit(run(os.Stdout, version))
}

func run(stdout io.Writer, version string) int {
	if _, err := fmt.Fprintf(stdout, "plumbline-verify %s\n", version); err != nil {
		return 1
	}
	return 0
}
