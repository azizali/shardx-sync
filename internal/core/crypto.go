package core

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"

	"golang.org/x/crypto/scrypt"
)

// File format of every encrypted object: "SXS1" | 12-byte nonce | AES-256-GCM ciphertext | 16-byte tag.
var magic = []byte("SXS1")

// DeriveKey turns the team passphrase into an AES-256 key. salt is the Neon URL, so the
// same passphrase gives different keys for different projects.
func DeriveKey(passphrase, salt string) ([]byte, error) {
	return scrypt.Key([]byte(passphrase), []byte("shardx-sync:"+salt), 1<<15, 8, 1, 32)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func Encrypt(key, plain []byte, aad string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(magic)+12+len(plain)+16)
	out = append(out, magic...)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plain, []byte(aad)), nil
}

func Decrypt(key, blob []byte, aad string) ([]byte, error) {
	if len(blob) < 32 || !bytes.Equal(blob[:4], magic) {
		return nil, Errf("CORRUPT", "Not a ShardX Sync file")
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, blob[4:16], blob[16:], []byte(aad))
	if err != nil {
		return nil, Errf("PASSPHRASE", "Could not decrypt. The team passphrase does not match the one used to upload.")
	}
	return plain, nil
}
