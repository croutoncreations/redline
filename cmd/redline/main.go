package main

import (
	"os"
	"time"

	"github.com/jfox/redline/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}
