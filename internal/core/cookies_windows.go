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
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

// dpapi wraps (protect) or unwraps (!protect) data for the current Windows user.
func dpapi(in []byte, protect bool) ([]byte, error) {
	if len(in) == 0 {
		return nil, errors.New("empty DPAPI blob")
	}
	inBlob := dataBlob{cbData: uint32(len(in)), pbData: &in[0]}
	var out dataBlob
	proc := procCryptUnprotectData
	if protect {
		proc = procCryptProtectData
	}
	r, _, err := proc.Call(uintptr(unsafe.Pointer(&inBlob)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	res := make([]byte, out.cbData)
	copy(res, unsafe.Slice(out.pbData, int(out.cbData)))
	return res, nil
}

// osCryptKey reads the cookie key from Local State. When there is none and mint is set (imports only),
// it creates one the way ShardBrowser does. Existing keys are never replaced: other data may depend on them.
func osCryptKey(udd string, mint bool) ([]byte, error) {
	path := filepath.Join(udd, "Local State")
	state := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &state); err != nil {
			return nil, errors.New("Local State is damaged")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	osc, _ := state["os_crypt"].(map[string]any)
	enc, _ := osc["encrypted_key"].(string)
	if enc == "" {
		if !mint {
			return nil, errors.New("this profile has no cookie key (Local State) yet")
		}
		return mintOSCryptKey(path, state)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) <= 5 {
		return nil, errors.New("Local State cookie key is damaged")
	}
	key, err := dpapi(raw[5:], false) // strip the "DPAPI" prefix
	if err != nil {
		return nil, errors.New("this computer cannot open the cookie key of this profile")
	}
	if len(key) != 32 {
		return nil, errors.New("cookie key has an unexpected length")
	}
	return key, nil
}

func mintOSCryptKey(path string, state map[string]any) ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	wrapped, err := dpapi(key, true)
	if err != nil {
		return nil, err
	}
	osc, _ := state["os_crypt"].(map[string]any)
	if osc == nil {
		osc = map[string]any{}
	}
	osc["encrypted_key"] = base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), wrapped...))
	state["os_crypt"] = osc
	b, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
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
