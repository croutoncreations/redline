//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

type entitlementCacheOperation struct {
	directory *os.File
	lock      *os.File
	base      string
}

func beginEntitlementCacheOperation(filePath string) (*entitlementCacheOperation, error) {
	return beginEntitlementCacheOperationContext(context.Background(), filePath)
}

func beginEntitlementCacheOperationContext(ctx context.Context, filePath string) (*entitlementCacheOperation, error) {
	directoryPath := filepath.Dir(filePath)
	if err := os.MkdirAll(directoryPath, 0o700); err != nil {
		return nil, fmt.Errorf("create entitlement cache directory: %w", err)
	}
	dfd, err := unix.Open(directoryPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open entitlement cache directory: %w", err)
	}
	directory := os.NewFile(uintptr(dfd), directoryPath)
	fail := func(err error) (*entitlementCacheOperation, error) {
		_ = directory.Close()
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(dfd, &stat); err != nil {
		return fail(fmt.Errorf("inspect entitlement cache directory: %w", err))
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() || stat.Mode&0o022 != 0 {
		return fail(errors.New("entitlement cache directory must be owner-controlled"))
	}
	base := filepath.Base(filePath)
	lfd, err := unix.Openat(dfd, "."+base+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail(fmt.Errorf("open entitlement cache lock: %w", err))
	}
	lock := os.NewFile(uintptr(lfd), "."+base+".lock")
	if err := unix.Fstat(lfd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Mode&0o777 != 0o600 {
		_ = lock.Close()
		return fail(errors.New("entitlement cache lock must be an owner-only regular file"))
	}
	for {
		err := unix.Flock(lfd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = lock.Close()
			return fail(fmt.Errorf("lock entitlement cache: %w", err))
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			return fail(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return &entitlementCacheOperation{directory: directory, lock: lock, base: base}, nil
}

func (op *entitlementCacheOperation) close() {
	_ = unix.Flock(int(op.lock.Fd()), unix.LOCK_UN)
	_ = op.lock.Close()
	_ = op.directory.Close()
}

func (op *entitlementCacheOperation) read() ([]byte, bool, error) {
	fd, err := unix.Openat(int(op.directory.Fd()), op.base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open entitlement cache: %w", err)
	}
	file := os.NewFile(uintptr(fd), op.base)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, false, fmt.Errorf("inspect entitlement cache: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Mode&0o777 != 0o600 {
		return nil, false, errors.New("entitlement cache must be an owner-only regular file with mode 0600")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxIssuerBody+1))
	if err != nil || len(raw) > maxIssuerBody {
		return nil, false, errors.New("read entitlement cache failed or exceeded limit")
	}
	return raw, true, nil
}

func (op *entitlementCacheOperation) syncDirectory() error {
	if err := entitlementCacheDirectorySync(op.directory); err != nil {
		return fmt.Errorf("sync entitlement cache directory: %w", err)
	}
	return nil
}

func (op *entitlementCacheOperation) remove() error {
	if err := unix.Unlinkat(int(op.directory.Fd()), op.base, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove entitlement file: %w", err)
	}
	return op.syncDirectory()
}

func (op *entitlementCacheOperation) write(raw []byte) error {
	name := ".relay-entitlement-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	fd, err := unix.Openat(int(op.directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary entitlement cache: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	published := false
	defer func() {
		if !published {
			_ = unix.Unlinkat(int(op.directory.Fd()), name, 0)
		}
	}()
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return fmt.Errorf("write entitlement cache: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync entitlement cache: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close entitlement cache: %w", err)
	}
	if err := unix.Renameat(int(op.directory.Fd()), name, int(op.directory.Fd()), op.base); err != nil {
		return fmt.Errorf("replace entitlement cache: %w", err)
	}
	published = true
	return op.syncDirectory()
}
