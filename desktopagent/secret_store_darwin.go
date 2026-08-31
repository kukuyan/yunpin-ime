// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

/*
#cgo CFLAGS: -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// SecKeychainSetUserInteractionAllowed changes process-wide legacy Keychain
// state. Serialize every YunPin Keychain operation so a non-interactive load
// cannot temporarily change the interaction policy underneath an interactive
// load, save, or delete in another goroutine.
static pthread_mutex_t yp_keychain_mutex = PTHREAD_MUTEX_INITIALIZER;

// Use volatile stores so the compiler cannot discard the wipe before free.
// This clears only YunPin's malloc copy. Security.framework owns its internal
// result object; YunPin cannot guarantee when or whether framework copies are
// overwritten.
static void yp_secure_free(unsigned char *value, size_t value_len) {
  if (value == NULL) return;
  volatile unsigned char *cursor = (volatile unsigned char *)value;
  while (value_len-- > 0) *cursor++ = 0;
  free(value);
}

static CFStringRef yp_string(const char *value, size_t length) {
  return CFStringCreateWithBytes(kCFAllocatorDefault, (const UInt8 *)value,
                                 (CFIndex)length, kCFStringEncodingUTF8, false);
}

static CFMutableDictionaryRef yp_query(const char *service, size_t service_len,
                                       const char *account, size_t account_len) {
  CFStringRef service_string = yp_string(service, service_len);
  CFStringRef account_string = yp_string(account, account_len);
  if (service_string == NULL || account_string == NULL) {
    if (service_string != NULL) CFRelease(service_string);
    if (account_string != NULL) CFRelease(account_string);
    return NULL;
  }
  CFMutableDictionaryRef query = CFDictionaryCreateMutable(
      kCFAllocatorDefault, 0, &kCFTypeDictionaryKeyCallBacks,
      &kCFTypeDictionaryValueCallBacks);
  if (query != NULL) {
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, service_string);
    CFDictionarySetValue(query, kSecAttrAccount, account_string);
    // The preview agent is an ad-hoc signed command-line tool. Apple's data
    // protection keychain requires provisioning-profile-authorized access
    // group entitlements, which this deliberately unsigned preview cannot
    // possess. SecItem defaults to the local file-based login keychain on
    // macOS when neither kSecUseDataProtectionKeychain nor synchronizable is
    // supplied. This keeps secrets encrypted and device-local while allowing
    // the per-user LaunchAgent to use the same item after login.
  }
  CFRelease(service_string);
  CFRelease(account_string);
  return query;
}

static int32_t yp_keychain_save(const char *service, size_t service_len,
                                const char *account, size_t account_len,
                                const unsigned char *value, size_t value_len) {
  CFMutableDictionaryRef query = yp_query(service, service_len, account, account_len);
  if (query == NULL) return errSecAllocate;
  CFDataRef data = CFDataCreate(kCFAllocatorDefault, value, (CFIndex)value_len);
  if (data == NULL) {
    CFRelease(query);
    return errSecAllocate;
  }
  CFMutableDictionaryRef changes = CFDictionaryCreateMutable(
      kCFAllocatorDefault, 0, &kCFTypeDictionaryKeyCallBacks,
      &kCFTypeDictionaryValueCallBacks);
  if (changes == NULL) {
    CFRelease(data);
    CFRelease(query);
    return errSecAllocate;
  }
  CFDictionarySetValue(changes, kSecValueData, data);
  pthread_mutex_lock(&yp_keychain_mutex);
  OSStatus status = SecItemUpdate(query, changes);
  if (status == errSecItemNotFound) {
    CFDictionarySetValue(query, kSecValueData, data);
    status = SecItemAdd(query, NULL);
  }
  pthread_mutex_unlock(&yp_keychain_mutex);
  CFRelease(changes);
  CFRelease(data);
  CFRelease(query);
  return status;
}

#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
static int32_t yp_keychain_load(const char *service, size_t service_len,
                                const char *account, size_t account_len,
                                unsigned char **value, size_t *value_len,
                                int32_t allow_authentication_ui) {
  *value = NULL;
  *value_len = 0;
  CFMutableDictionaryRef query = yp_query(service, service_len, account, account_len);
  if (query == NULL) return errSecAllocate;
  CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
  CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
  if (!allow_authentication_ui) {
    CFDictionarySetValue(query, kSecUseAuthenticationUI,
                         kSecUseAuthenticationUIFail);
  }
  CFTypeRef result = NULL;
  pthread_mutex_lock(&yp_keychain_mutex);
  OSStatus status = errSecSuccess;
  Boolean interaction_was_allowed = false;
  Boolean changed_legacy_interaction = false;
  if (!allow_authentication_ui) {
    // Keep the modern per-query fail-closed flag above and add the legacy
    // process-wide interaction gate as defense in depth. YunPin preview builds
    // intentionally use the file-based login Keychain, whose authorization
    // path previously displayed SecurityAgent despite the query flag.
    status = SecKeychainGetUserInteractionAllowed(&interaction_was_allowed);
    if (status == errSecSuccess && interaction_was_allowed) {
      status = SecKeychainSetUserInteractionAllowed(false);
      changed_legacy_interaction = status == errSecSuccess;
    }
  }
  if (status == errSecSuccess) {
    status = SecItemCopyMatching(query, &result);
  }
  OSStatus restore_status = errSecSuccess;
  if (changed_legacy_interaction) {
    restore_status = SecKeychainSetUserInteractionAllowed(true);
  }
  pthread_mutex_unlock(&yp_keychain_mutex);
  CFRelease(query);
  if (restore_status != errSecSuccess) {
    if (result != NULL) CFRelease(result);
    return restore_status;
  }
  if (status != errSecSuccess) {
    if (result != NULL) CFRelease(result);
    return status;
  }
  if (result == NULL || CFGetTypeID(result) != CFDataGetTypeID()) {
    if (result != NULL) CFRelease(result);
    return errSecDecode;
  }
  CFDataRef data = (CFDataRef)result;
  CFIndex length = CFDataGetLength(data);
  if (length < 1 || length > 65536) {
    CFRelease(result);
    return errSecDecode;
  }
  unsigned char *copy = (unsigned char *)malloc((size_t)length);
  if (copy == NULL) {
    CFRelease(result);
    return errSecAllocate;
  }
  memcpy(copy, CFDataGetBytePtr(data), (size_t)length);
  *value = copy;
  *value_len = (size_t)length;
  CFRelease(result);
  return errSecSuccess;
}
#pragma clang diagnostic pop

static int32_t yp_keychain_delete(const char *service, size_t service_len,
                                  const char *account, size_t account_len) {
  CFMutableDictionaryRef query = yp_query(service, service_len, account, account_len);
  if (query == NULL) return errSecAllocate;
  pthread_mutex_lock(&yp_keychain_mutex);
  OSStatus status = SecItemDelete(query);
  pthread_mutex_unlock(&yp_keychain_mutex);
  CFRelease(query);
  return status;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"unsafe"
)

const keychainItemNotFound = -25300

type keychainSecretStore struct {
	service string
}

func NewPlatformSecretStore(options PlatformSecretStoreOptions) (SecretStore, error) {
	if err := validateStoreOptions(options); err != nil {
		return nil, err
	}
	return &keychainSecretStore{service: options.Service}, nil
}

func keychainStrings(service, profile string) (*C.char, *C.char, error) {
	if err := validateProfile(profile); err != nil {
		return nil, nil, err
	}
	serviceString := C.CString(service)
	profileString := C.CString(profile)
	if serviceString == nil || profileString == nil {
		if serviceString != nil {
			C.free(unsafe.Pointer(serviceString))
		}
		if profileString != nil {
			C.free(unsafe.Pointer(profileString))
		}
		return nil, nil, errors.New("allocate Keychain query")
	}
	return serviceString, profileString, nil
}

func (store *keychainSecretStore) Save(ctx context.Context, profile string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(value) < 1 || len(value) > maxCredentialBlobBytes {
		return errors.New("credential value length is invalid")
	}
	service, account, err := keychainStrings(store.service, profile)
	if err != nil {
		return err
	}
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))
	status := C.yp_keychain_save(
		service, C.size_t(len(store.service)), account, C.size_t(len(profile)),
		(*C.uchar)(unsafe.Pointer(&value[0])), C.size_t(len(value)),
	)
	if status != 0 {
		return fmt.Errorf("save YunPin credential in Keychain: OSStatus %d", int32(status))
	}
	// Do not turn a successful OS commit into an ambiguous cancellation error.
	return nil
}

func (store *keychainSecretStore) load(ctx context.Context, profile string, allowAuthenticationUI bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	service, account, err := keychainStrings(store.service, profile)
	if err != nil {
		return nil, err
	}
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))
	var value *C.uchar
	var length C.size_t
	allowUI := C.int32_t(0)
	if allowAuthenticationUI {
		allowUI = 1
	}
	status := C.yp_keychain_load(
		service, C.size_t(len(store.service)), account, C.size_t(len(profile)), &value, &length, allowUI,
	)
	if int32(status) == keychainItemNotFound {
		return nil, ErrSecretNotFound
	}
	if status != 0 {
		return nil, fmt.Errorf("load YunPin credential from Keychain: OSStatus %d", int32(status))
	}
	defer C.yp_secure_free(value, length)
	if length < 1 || length > maxCredentialBlobBytes {
		return nil, errors.New("Keychain returned an invalid credential length")
	}
	result := C.GoBytes(unsafe.Pointer(value), C.int(length))
	if err := ctx.Err(); err != nil {
		zeroBytes(result)
		return nil, err
	}
	return result, nil
}

func (store *keychainSecretStore) Load(ctx context.Context, profile string) ([]byte, error) {
	return store.load(ctx, profile, true)
}

func (store *keychainSecretStore) LoadWithoutUserInteraction(ctx context.Context, profile string) ([]byte, error) {
	return store.load(ctx, profile, false)
}

func (store *keychainSecretStore) Delete(ctx context.Context, profile string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	service, account, err := keychainStrings(store.service, profile)
	if err != nil {
		return err
	}
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))
	status := C.yp_keychain_delete(
		service, C.size_t(len(store.service)), account, C.size_t(len(profile)),
	)
	if int32(status) == keychainItemNotFound {
		return ErrSecretNotFound
	}
	if status != 0 {
		return fmt.Errorf("delete YunPin credential from Keychain: OSStatus %d", int32(status))
	}
	return nil
}
