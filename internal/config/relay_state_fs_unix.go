//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// Narrow test seams let durability tests interrupt immediately around rename;
// production values are no-ops and are not exported.
var (
	relayStateBeforeRename = func() error { return nil }
	relayStateAfterRename  = func() error { return nil }
)

type relayStateOperation struct {
	directory *os.File
	lock      *os.File
	base      string
	committed bool
}

func beginRelayStateOperation(path string) (*relayStateOperation, error) {
	directoryPath := filepath.Dir(path)
	if err := os.MkdirAll(directoryPath, 0o700); err != nil {
		return nil, fmt.Errorf("create relay state directory: %w", err)
	}
	dfd, err := unix.Open(directoryPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open relay state directory: %w", err)
	}
	directory := os.NewFile(uintptr(dfd), directoryPath)
	failure := func(err error) (*relayStateOperation, error) {
		if closeErr := directory.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close relay state directory: %w", closeErr))
		}
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(dfd, &stat); err != nil {
		return failure(fmt.Errorf("inspect relay state directory: %w", err))
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return failure(fmt.Errorf("relay state parent must be a directory"))
	}
	if int(stat.Uid) != os.Geteuid() {
		return failure(fmt.Errorf("relay state directory must be owned by uid %d", os.Geteuid()))
	}
	if stat.Mode&0o022 != 0 {
		return failure(fmt.Errorf("relay state directory permissions %#o are invalid; group and other write access is forbidden", stat.Mode&0o777))
	}

	base := filepath.Base(path)
	lockName := "." + base + ".lock"
	lfd := -1
	for attempt := 0; attempt < 100; attempt++ {
		lfd, err = unix.Openat(dfd, lockName, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.EINTR) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		return failure(fmt.Errorf("open relay state lock: %w", err))
	}
	lock := os.NewFile(uintptr(lfd), filepath.Join(directoryPath, lockName))
	lockFailure := func(err error) (*relayStateOperation, error) {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close relay state lock: %w", closeErr))
		}
		return failure(err)
	}
	if err := unix.Fstat(lfd, &stat); err != nil {
		return lockFailure(fmt.Errorf("inspect relay state lock: %w", err))
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Mode&0o777 != 0o600 {
		return lockFailure(fmt.Errorf("relay state lock must be an owner-only regular file"))
	}
	if err := unix.Flock(lfd, unix.LOCK_EX); err != nil {
		return lockFailure(fmt.Errorf("lock relay state: %w", err))
	}
	return &relayStateOperation{directory: directory, lock: lock, base: base}, nil
}

func (op *relayStateOperation) close() error {
	var result error
	if err := unix.Flock(int(op.lock.Fd()), unix.LOCK_UN); err != nil {
		result = errors.Join(result, fmt.Errorf("unlock relay state: %w", err))
	}
	if err := op.lock.Close(); err != nil {
		result = errors.Join(result, fmt.Errorf("close relay state lock: %w", err))
	}
	if err := op.directory.Close(); err != nil {
		result = errors.Join(result, fmt.Errorf("close relay state directory: %w", err))
	}
	if result != nil && op.committed {
		return &RelayStateCommitError{Err: result}
	}
	return result
}

func (op *relayStateOperation) read() ([]byte, bool, error) {
	fd, err := unix.Openat(int(op.directory.Fd()), op.base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open relay state: %w", err)
	}
	file := os.NewFile(uintptr(fd), op.base)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, false, fmt.Errorf("inspect opened relay state: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = file.Close()
		return nil, false, fmt.Errorf("relay state must be a regular file")
	}
	if int(stat.Uid) != os.Geteuid() {
		_ = file.Close()
		return nil, false, fmt.Errorf("relay state must be owned by uid %d", os.Geteuid())
	}
	if stat.Mode&0o777 != 0o600 {
		_ = file.Close()
		return nil, false, fmt.Errorf("relay state permissions %#o are invalid; want 0600", stat.Mode&0o777)
	}
	raw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, false, fmt.Errorf("read relay state: %w", readErr)
	}
	if closeErr != nil {
		return nil, false, fmt.Errorf("close relay state: %w", closeErr)
	}
	return raw, true, nil
}

func (op *relayStateOperation) write(raw []byte) error {
	var temporaryName string
	fd := -1
	for attempt := 0; attempt < 100; attempt++ {
		temporaryName = ".relay-state-" + strconv.Itoa(os.Getpid()) + "-" + strconv.Itoa(attempt)
		opened, err := unix.Openat(int(op.directory.Fd()), temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create temporary relay state: %w", err)
		}
		fd = opened
		break
	}
	if fd < 0 {
		return fmt.Errorf("create temporary relay state: exhausted exclusive names")
	}
	file := os.NewFile(uintptr(fd), temporaryName)
	published := false
	defer func() {
		if !published {
			_ = unix.Unlinkat(int(op.directory.Fd()), temporaryName, 0)
		}
	}()
	// Set the exact final mode through the still-exclusive descriptor before
	// any bytes are written or the name is published.
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect temporary relay state: %w", err)
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary relay state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary relay state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary relay state: %w", err)
	}
	if err := relayStateBeforeRename(); err != nil {
		return fmt.Errorf("before replacing relay state: %w", err)
	}
	if err := unix.Renameat(int(op.directory.Fd()), temporaryName, int(op.directory.Fd()), op.base); err != nil {
		return fmt.Errorf("replace relay state: %w", err)
	}
	published = true
	op.committed = true
	if err := relayStateAfterRename(); err != nil {
		return &RelayStateCommitError{Err: fmt.Errorf("after replacing relay state: %w", err)}
	}
	if err := op.directory.Sync(); err != nil {
		return &RelayStateCommitError{Err: fmt.Errorf("sync relay state directory: %w", err)}
	}
	return nil
}
