//go:build darwin && cgo

package config

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static CFStringRef redline_string(const char *value) {
	return CFStringCreateWithCString(kCFAllocatorDefault, value, kCFStringEncodingUTF8);
}

static CFMutableDictionaryRef redline_query(const char *service, const char *account) {
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (query == NULL) return NULL;
	CFStringRef serviceValue = redline_string(service);
	CFStringRef accountValue = redline_string(account);
	if (serviceValue == NULL || accountValue == NULL) {
		if (serviceValue != NULL) CFRelease(serviceValue);
		if (accountValue != NULL) CFRelease(accountValue);
		CFRelease(query);
		return NULL;
	}
	CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(query, kSecAttrService, serviceValue);
	CFDictionarySetValue(query, kSecAttrAccount, accountValue);
	CFRelease(serviceValue);
	CFRelease(accountValue);
	return query;
}

static OSStatus redline_license_load(const char *service, const char *account, void **output, CFIndex *length) {
	*output = NULL;
	*length = 0;
	CFMutableDictionaryRef query = redline_query(service, account);
	if (query == NULL) return errSecAllocate;
	CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef result = NULL;
	OSStatus status = SecItemCopyMatching(query, &result);
	CFRelease(query);
	if (status != errSecSuccess) return status;
	if (result == NULL || CFGetTypeID(result) != CFDataGetTypeID()) {
		if (result != NULL) CFRelease(result);
		return errSecDecode;
	}
	CFDataRef data = (CFDataRef)result;
	CFIndex size = CFDataGetLength(data);
	void *copy = malloc((size_t)(size == 0 ? 1 : size));
	if (copy == NULL) {
		CFRelease(result);
		return errSecAllocate;
	}
	if (size > 0) memcpy(copy, CFDataGetBytePtr(data), (size_t)size);
	CFRelease(result);
	*output = copy;
	*length = size;
	return errSecSuccess;
}

static OSStatus redline_license_replace(const char *service, const char *account, const void *bytes, CFIndex length) {
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, bytes, length);
	if (data == NULL) return errSecAllocate;
	CFMutableDictionaryRef query = redline_query(service, account);
	if (query == NULL) {
		CFRelease(data);
		return errSecAllocate;
	}
	const void *keys[] = { kSecValueData };
	const void *values[] = { data };
	CFDictionaryRef update = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (update == NULL) {
		CFRelease(query);
		CFRelease(data);
		return errSecAllocate;
	}
	OSStatus status = SecItemUpdate(query, update);
	CFRelease(update);
	if (status == errSecItemNotFound) {
		CFDictionarySetValue(query, kSecValueData, data);
		status = SecItemAdd(query, NULL);
		if (status == errSecDuplicateItem) {
			const void *retryKeys[] = { kSecValueData };
			const void *retryValues[] = { data };
			CFDictionaryRef retry = CFDictionaryCreate(kCFAllocatorDefault, retryKeys, retryValues, 1,
				&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
			if (retry == NULL) {
				CFRelease(query);
				CFRelease(data);
				return errSecAllocate;
			}
			CFDictionaryRemoveValue(query, kSecValueData);
			status = SecItemUpdate(query, retry);
			CFRelease(retry);
		}
	}
	CFRelease(query);
	CFRelease(data);
	return status;
}

static OSStatus redline_license_clear(const char *service, const char *account) {
	CFMutableDictionaryRef query = redline_query(service, account);
	if (query == NULL) return errSecAllocate;
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	return status;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"unsafe"
)

// Security.framework performs reads and mutations without exposing the license
// in argv, environment variables, or command output. The process-local lock
// also makes Replace/Clear atomic from concurrent callers' point of view.
var relayLicenseKeychainMu sync.Mutex

type keychainLicenseStore struct{ service, account string }

func newPlatformLicenseStore() LicenseStore {
	return keychainLicenseStore{service: RelayLicenseKeychainService, account: RelayLicenseKeychainAccount}
}

// newKeychainLicenseStore allows a test to use a unique synthetic item without
// ever reading or mutating the production service/account pair.
func newKeychainLicenseStore(service, account string) LicenseStore {
	return keychainLicenseStore{service: service, account: account}
}

func keychainStatusError(operation string, status C.OSStatus) error {
	if status == C.errSecItemNotFound {
		return ErrLicenseNotFound
	}
	return fmt.Errorf("%s hosted relay license in Keychain: %w (OSStatus %d)", operation, ErrLicenseStoreUnavailable, int32(status))
}

func (s keychainLicenseStore) identifiers() (string, string) {
	service, account := s.service, s.account
	if service == "" {
		service = RelayLicenseKeychainService
	}
	if account == "" {
		account = RelayLicenseKeychainAccount
	}
	return service, account
}

func cIdentifiers(serviceName, accountName string) (*C.char, *C.char, error) {
	service := C.CString(serviceName)
	account := C.CString(accountName)
	if service == nil || account == nil {
		if service != nil {
			C.free(unsafe.Pointer(service))
		}
		if account != nil {
			C.free(unsafe.Pointer(account))
		}
		return nil, nil, errors.New("allocate Keychain item identifiers")
	}
	return service, account, nil
}

func (s keychainLicenseStore) Load(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	relayLicenseKeychainMu.Lock()
	defer relayLicenseKeychainMu.Unlock()
	serviceName, accountName := s.identifiers()
	service, account, err := cIdentifiers(serviceName, accountName)
	if err != nil {
		return "", err
	}
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))
	var output unsafe.Pointer
	var length C.CFIndex
	status := C.redline_license_load(service, account, &output, &length)
	if status != C.errSecSuccess {
		return "", keychainStatusError("read", status)
	}
	defer C.free(output)
	if length < 0 || uint64(length) > uint64(math.MaxInt32) {
		return "", fmt.Errorf("read hosted relay license from Keychain: invalid data length %d", int64(length))
	}
	value := C.GoBytes(output, C.int(length))
	if len(value) == 0 {
		return "", ErrLicenseNotFound
	}
	return string(value), nil
}

func (s keychainLicenseStore) Replace(ctx context.Context, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("replace hosted relay license: value is empty; use Clear to remove it")
	}
	if uint64(len(value)) > uint64(math.MaxInt64) {
		return errors.New("replace hosted relay license: value is too large")
	}
	relayLicenseKeychainMu.Lock()
	defer relayLicenseKeychainMu.Unlock()
	serviceName, accountName := s.identifiers()
	service, account, err := cIdentifiers(serviceName, accountName)
	if err != nil {
		return err
	}
	secret := C.CBytes([]byte(value))
	if secret == nil {
		C.free(unsafe.Pointer(service))
		C.free(unsafe.Pointer(account))
		return errors.New("allocate Keychain replacement value")
	}
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))
	defer C.free(secret)
	status := C.redline_license_replace(service, account, secret, C.CFIndex(len(value)))
	if status != C.errSecSuccess {
		return keychainStatusError("replace", status)
	}
	return nil
}

func (s keychainLicenseStore) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	relayLicenseKeychainMu.Lock()
	defer relayLicenseKeychainMu.Unlock()
	serviceName, accountName := s.identifiers()
	service, account, err := cIdentifiers(serviceName, accountName)
	if err != nil {
		return err
	}
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))
	status := C.redline_license_clear(service, account)
	if status == C.errSecItemNotFound {
		return nil
	}
	if status != C.errSecSuccess {
		return keychainStatusError("clear", status)
	}
	return nil
}
