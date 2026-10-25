// A tiny program used by the tests as a stand-in for a real release
// binary. The version is set at build time with -ldflags "-X main.version=...".
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("hello " + version)
		return
	}
	fmt.Println("hello")
}
