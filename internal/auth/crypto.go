package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

// keychainKey fetches the AES key that `sf`/`sfdx` uses to encrypt tokens.
//
// On macOS it is stored as a generic password in the login Keychain under
// service=sfdx, account=local. The value is a 32-character ASCII string, used
// directly (not hex-decoded) as a 32-byte AES-256 key — matching how Node's
// crypto.createCipheriv treats the string.
func keychainKey() ([]byte, error) {
	out, err := exec.Command("security",
		"find-generic-password", "-s", "sfdx", "-a", "local", "-w").Output()
	if err != nil {
		return nil, fmt.Errorf("read sfdx key from Keychain (service=sfdx, account=local): %w", err)
	}
	key := strings.TrimRight(string(out), "\r\n")
	if len(key) != 32 {
		return nil, fmt.Errorf("unexpected sfdx key length %d (want 32)", len(key))
	}
	return []byte(key), nil
}

// decryptSecret decrypts a value stored by sfdx in the form
//
//	<iv:12 hex chars><ciphertext hex>:<authTag:32 hex chars>
//
// The IV is the 12-character hex string used as 12 raw ASCII bytes (the GCM
// nonce), and the auth tag is the trailing 16 bytes after the ':' delimiter.
func decryptSecret(key []byte, enc string) (string, error) {
	const ivLen = 12 // chars == bytes, used as the GCM nonce
	colon := strings.IndexByte(enc, ':')
	if colon < ivLen {
		return "", fmt.Errorf("malformed encrypted value")
	}
	nonce := []byte(enc[:ivLen])
	ciphertext, err := hex.DecodeString(enc[ivLen:colon])
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	tag, err := hex.DecodeString(enc[colon+1:])
	if err != nil {
		return "", fmt.Errorf("decode auth tag: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block) // standard 12-byte nonce, 16-byte tag
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, append(ciphertext, tag...), nil)
	if err != nil {
		return "", fmt.Errorf("gcm open: %w", err)
	}
	return string(plaintext), nil
}
