package apiauth

import "os"

// CreatePrivateFile creates path exclusively so that only the current user
// (and SYSTEM on Windows) can open it, private from the moment it exists.
func CreatePrivateFile(path string) (*os.File, error) { return createPrivateFile(path) }
