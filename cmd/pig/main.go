package main

import (
	"os"

	_ "github.com/MichaelKinsy/PiG/ai/all"
	"github.com/MichaelKinsy/PiG/coding/cli"
	"github.com/MichaelKinsy/PiG/coding/extension"
	_ "github.com/MichaelKinsy/PiG/coding/extension/host/lispcell" // builds Lisp extensions, which an embedding program of the extension host does not
	directdemo "github.com/MichaelKinsy/PiG/examples/extensions/direct-go-demo"
)

// Ports packages/coding-agent/src/cli.ts

func main() {
	var options *cli.MainOptions
	if os.Getenv(directdemo.LogEnv) != "" {
		options = &cli.MainOptions{ExtensionFactories: []extension.InlineExtension{
			extension.NamedInlineExtension{Name: "direct-go-demo", Factory: directdemo.Extension, Builtin: true},
		}}
	}
	cli.Main(os.Args[1:], options)
}
