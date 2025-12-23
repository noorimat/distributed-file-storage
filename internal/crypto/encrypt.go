package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

const (
	KeySize   = 32 // AES-256 requires 32 byte key
	SaltSize  = 32 // Salt for key derivation
	NonceSize = 12 // GCM standard nonce size
	Iterations = 100000 // PBKDF2 iterations for key derivation
)

// EncryptionKey represents a derived encryption key
type EncryptionKey struct {
	Key  []byte
	Salt []byte
}

// DeriveKey derives an encryption key from a password using PBKDF2
// This allows users to encrypt files with a password instead of managing raw keys
func DeriveKey(password string, salt []byte) (*EncryptionKey, error) {
	if salt == nil {
		// Generate new salt if not provided
		salt = make([]byte, SaltSize)
		if _, err := io.ReadFull(rand.Reader, salt); err != nil {
			return nil, err
		}
	}

	// Use PBKDF2 to derive a key from the password
	// This makes brute-force attacks computationally expensive
	key := pbkdf2.Key([]byte(password), salt, Iterations, KeySize, sha256.New)

	return &EncryptionKey{
		Key:  key,
		Salt: salt,
	}, nil
}

// EncryptChunk encrypts a chunk using AES-256-GCM
// GCM provides both encryption and authentication (AEAD)
func EncryptChunk(data []byte, key *EncryptionKey) ([]byte, error) {
	// Create AES cipher
	block, err := aes.NewCipher(key.Key)
	if err != nil {
		return nil, err
	}

	// Use GCM mode (Galois/Counter Mode)
	// GCM provides authenticated encryption - it detects tampering
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Generate a random nonce (number used once)
	// Critical: Never reuse a nonce with the same key
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// Encrypt and authenticate the data
	// The nonce is prepended to the ciphertext so we can decrypt later
	ciphertext := gcm.Seal(nonce, nonce, data, nil)

	return ciphertext, nil
}

// DecryptChunk decrypts a chunk encrypted with EncryptChunk
func DecryptChunk(ciphertext []byte, key *EncryptionKey) ([]byte, error) {
	// Create AES cipher
	block, err := aes.NewCipher(key.Key)
	if err != nil {
		return nil, err
	}

	// Use GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Extract nonce from the beginning of ciphertext
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce := ciphertext[:nonceSize]
	ciphertext = ciphertext[nonceSize:]

	// Decrypt and verify authentication tag
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// HashPassword creates a deterministic hash from password for server-side verification
// This does NOT store the actual password
func HashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

// EncryptedChunkMetadata stores encryption information for a chunk
type EncryptedChunkMetadata struct {
	IsEncrypted bool   `json:"is_encrypted"`
	Salt        string `json:"salt,omitempty"`        // Hex-encoded salt for key derivation
	Algorithm   string `json:"algorithm,omitempty"`   // "AES-256-GCM"
}