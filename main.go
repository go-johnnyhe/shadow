package main

import (
	"github.com/go-johnnyhe/shadow/cmd"
)

var version = "dev"

func main() {
	cmd.Version = version
	cmd.Execute()
}
