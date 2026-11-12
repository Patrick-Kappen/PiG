// Command raw-args runs the raw tool-argument conformance row's Go extension as a standalone executable, so the host gives it its own isolated process.
package main

import (
	"fmt"
	"os"

	"github.com/MichaelKinsy/PiG/test/extension-conformance/testfixture/rawargs"
)

func main() {
	if err := rawargs.Extension("raw-args").Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "raw-args: %v\n", err)
		os.Exit(1)
	}
}
