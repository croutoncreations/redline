//go:build tools

// Package tools pins build-time dependencies that no production code imports.
//
// gomobile is invoked as an external binary by scripts/build-mobile-core.sh
// rather than imported, so without this file `go mod tidy` prunes
// golang.org/x/mobile from go.mod and the mobile build stops resolving.
// The build tag keeps it out of every normal build.
package tools

import (
	_ "golang.org/x/mobile/bind"
)
