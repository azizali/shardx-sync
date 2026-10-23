//go:build windows

package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Windows keeps the cookie key in "Local State" wrapped with DPAPI, so it only opens for the Windows
// user that wrote it. That is why cookies travel as cookies.json instead of the raw database.
// Not tested on a Windows machine yet.

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func dpapiUnprotect(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return nil, errors.New("empty DPAPI blob")
	}
	inBlob := dataBlob{cbData: uint32(len(in)), pbData: &in[0]}
	var out dataBlob
	r, _, err := procCryptUnprotectData.Call(uintptr(unsafe.Pointer(&inBlob)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	res := make([]byte, out.cbData)
	copy(res, unsafe.Slice(out.pbData, int(out.cbData)))
	return res, nil
}

func osCryptKey(udd string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(udd, "Local State"))
	if err != nil {
		return nil, errors.New("this profile has no cookie key (Local State) yet")
	}
	var state struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(b, &state); err != nil || state.OSCrypt.EncryptedKey == "" {
		return nil, errors.New("Local State has no cookie key")
	}
	raw, err := base64.StdEncoding.DecodeString(state.OSCrypt.EncryptedKey)
	if err != nil || len(raw) <= 5 {
		return nil, errors.New("Local State cookie key is damaged")
	}
	key, err := dpapiUnprotect(raw[5:]) // strip the "DPAPI" prefix
	if err != nil {
		return nil, errors.New("this computer cannot open the cookie key of this profile")
	}
	if len(key) != 32 {
		return nil, errors.New("cookie key has an unexpected length")
	}
	return key, nil
}

// v10 body = nonce(12) || ciphertext || tag(16)
func (c cookieCrypt) decrypt(body []byte) ([]byte, bool) {
	gcm, ok := cookieAEAD(c.key)
	if !ok || len(body) < gcm.NonceSize()+gcm.Overhead() {
		return nil, false
	}
	pt, err := gcm.Open(nil, body[:gcm.NonceSize()], body[gcm.NonceSize():], nil)
	return pt, err == nil
}

func (c cookieCrypt) encrypt(plain []byte) []byte {
	gcm, _ := cookieAEAD(c.key)
	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)
	return gcm.Seal(nonce, nonce, plain, nil)
}

func cookieAEAD(key []byte) (cipher.AEAD, bool) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	gcm, err := cipher.NewGCM(block)
	return gcm, err == nil
}
