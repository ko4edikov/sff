package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// keychainKey fetches the 32-character AES key that `sf`/`sfdx` uses to encrypt
// tokens, reading it from wherever the current platform's sf stores it:
//
//   - macOS: the login Keychain (service=sfdx, account=local), via `security`.
//   - Linux: libsecret, via `secret-tool`.
//   - Windows (and any headless/fallback): the file ~/.sfdx/key.json.
//
// If the native store fails, it falls back to ~/.sfdx/key.json, mirroring sf's
// own generic-keychain fallback. The value is a 32-character ASCII string used
// directly (not hex-decoded) as a 32-byte AES-256 key, matching how Node's
// crypto.createCipheriv treats the string.
func keychainKey() ([]byte, error) {
	var key string
	var nativeErr error
	switch runtime.GOOS {
	case "darwin":
		key, nativeErr = macKeychainKey()
	case "linux":
		key, nativeErr = secretToolKey()
	}

	if key == "" {
		fileKey, fileErr := keyFromFile()
		if fileErr != nil {
			if nativeErr != nil {
				return nil, nativeErr // surface the native error on platforms that have one
			}
			return nil, fileErr
		}
		key = fileKey
	}

	if len(key) != 32 {
		return nil, fmt.Errorf("unexpected sfdx key length %d (want 32)", len(key))
	}
	return []byte(key), nil
}

// macKeychainKey reads the key from the macOS login Keychain.
func macKeychainKey() (string, error) {
	out, err := exec.Command("security",
		"find-generic-password", "-s", "sfdx", "-a", "local", "-w").Output()
	if err != nil {
		return "", fmt.Errorf("read sfdx key from Keychain (service=sfdx, account=local): %w", err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// secretToolKey reads the key from the Linux libsecret store via secret-tool,
// matching sf's `secret-tool lookup user local domain sfdx`.
func secretToolKey() (string, error) {
	prog := os.Getenv("SFDX_SECRET_TOOL_PATH")
	if prog == "" {
		prog = "secret-tool"
	}
	out, err := exec.Command(prog, "lookup", "user", "local", "domain", "sfdx").Output()
	if err != nil {
		return "", fmt.Errorf("read sfdx key via secret-tool: %w", err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// keyFromFile reads sf's generic file keychain, ~/.sfdx/key.json — the store used
// on Windows and as a fallback elsewhere. Its shape is
// {"account":"local","key":"<32 chars>","service":"sfdx"}.
func keyFromFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(home, ".sfdx", "key.json"))
	if err != nil {
		return "", fmt.Errorf("read ~/.sfdx/key.json: %w", err)
	}
	var f struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return "", fmt.Errorf("parse ~/.sfdx/key.json: %w", err)
	}
	if f.Key == "" {
		return "", fmt.Errorf("~/.sfdx/key.json has no key")
	}
	return f.Key, nil
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
