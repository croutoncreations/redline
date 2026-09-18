package main

import (
	"os"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}
