//go:build !windows

package core

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"runtime"
)

// osCryptKey is the key ShardBrowser derives on macOS and Linux. It is fixed, not stored per computer,
// so a cookie store written on one Mac or Linux machine opens on another (see src-tauri/src/cookies.rs).
// mint is only used on Windows, where a missing key has to be created.
func osCryptKey(_ string, _ bool) ([]byte, error) {
	pass, iter := "peanuts", 1
	if runtime.GOOS == "darwin" {
		pass, iter = "mock_password", 1003
	}
	return pbkdf2.Key(sha1.New, pass, []byte("saltysalt"), iter, 16)
}

var cookieIV = bytes.Repeat([]byte{0x20}, aes.BlockSize)

func (c cookieCrypt) decrypt(body []byte) ([]byte, bool) {
	block, err := aes.NewCipher(c.key)
	if err != nil || len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return nil, false
	}
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, cookieIV).CryptBlocks(out, body)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) {
		return nil, false
	}
	for _, b := range out[len(out)-pad:] {
		if int(b) != pad {
			return nil, false
		}
	}
	return out[:len(out)-pad], true
}

func (c cookieCrypt) encrypt(plain []byte) []byte {
	block, _ := aes.NewCipher(c.key)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, cookieIV).CryptBlocks(out, padded)
	return out
}
