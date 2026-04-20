package irc

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/blowfish"
)

// BlowfishEncryptor handles Blowfish encryption/decryption
type BlowfishEncryptor struct {
	cipher cipher.Block
	mode   string // "ECB" or "CBC"
}

// NewBlowfishEncryptor creates a new encryptor with a key
// Key format: "cbc:keystring" or "ecb:keystring" or just "keystring" (defaults to ECB)
func NewBlowfishEncryptor(keyStr string) (*BlowfishEncryptor, error) {
	mode := "ECB"
	key := keyStr
	
	// Parse mode prefix
	if strings.HasPrefix(keyStr, "cbc:") {
		mode = "CBC"
		key = keyStr[4:]
	} else if strings.HasPrefix(keyStr, "ecb:") {
		mode = "ECB"
		key = keyStr[4:]
	}
	
	// Create cipher
	c, err := blowfish.NewCipher([]byte(key))
	if err != nil {
		return nil, err
	}
	
	return &BlowfishEncryptor{
		cipher: c,
		mode:   mode,
	}, nil
}

// Encrypt encrypts plaintext and returns base64 encoded ciphertext
func (b *BlowfishEncryptor) Encrypt(plaintext string) string {
	data := []byte(plaintext)
	
	if b.mode == "CBC" {
		return b.encryptCBC(data)
	}
	return b.encryptECB(data)
}

// Decrypt decrypts base64 encoded ciphertext
func (b *BlowfishEncryptor) Decrypt(ciphertext string) (string, error) {
	if b.mode == "CBC" {
		return b.decryptCBC(ciphertext)
	}
	return b.decryptECB(ciphertext)
}

// encryptECB encrypts in ECB mode (simple block by block)
func (b *BlowfishEncryptor) encryptECB(data []byte) string {
	// Pad to block size
	blockSize := b.cipher.BlockSize()
	padLen := blockSize - (len(data) % blockSize)
	for i := 0; i < padLen; i++ {
		data = append(data, byte(padLen))
	}
	
	// Encrypt blocks
	for i := 0; i < len(data); i += blockSize {
		b.cipher.Encrypt(data[i:i+blockSize], data[i:i+blockSize])
	}
	
	// Base64 encode
	return base64.StdEncoding.EncodeToString(data)
}

// decryptECB decrypts in ECB mode
func (b *BlowfishEncryptor) decryptECB(ciphertext string) (string, error) {
	// Base64 decode
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	
	// Decrypt blocks
	blockSize := b.cipher.BlockSize()
	for i := 0; i < len(data); i += blockSize {
		b.cipher.Decrypt(data[i:i+blockSize], data[i:i+blockSize])
	}
	
	// Remove padding
	if len(data) > 0 {
		padLen := int(data[len(data)-1])
		if padLen <= blockSize && padLen > 0 {
			data = data[:len(data)-padLen]
		}
	}
	
	return string(data), nil
}

// encryptCBC encrypts in CBC mode (FiSH-compatible: random IV prepended, zero-padded)
func (b *BlowfishEncryptor) encryptCBC(data []byte) string {
	blockSize := b.cipher.BlockSize()
	// FiSH/Mircryption use zero-padding for CBC
	if rem := len(data) % blockSize; rem != 0 {
		padLen := blockSize - rem
		for i := 0; i < padLen; i++ {
			data = append(data, 0)
		}
	}

	// Generate a random IV and prepend it to the ciphertext
	iv := make([]byte, blockSize)
	if _, err := rand.Read(iv); err != nil {
		// Fallback to sha1 of plaintext if rand fails
		h := sha1.Sum(data)
		copy(iv, h[:blockSize])
	}

	mode := cipher.NewCBCEncrypter(b.cipher, iv)
	mode.CryptBlocks(data, data)

	out := make([]byte, 0, len(iv)+len(data))
	out = append(out, iv...)
	out = append(out, data...)
	return base64.StdEncoding.EncodeToString(out)
}

// decryptCBC decrypts in CBC mode
func (b *BlowfishEncryptor) decryptCBC(ciphertext string) (string, error) {
	// Base64 decode
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	
	if len(data) < b.cipher.BlockSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	
	// Use first block as IV
	blockSize := b.cipher.BlockSize()
	iv := data[:blockSize]
	
	// Decrypt
	mode := cipher.NewCBCDecrypter(b.cipher, iv)
	mode.CryptBlocks(data[blockSize:], data[blockSize:])
	
	plain := data[blockSize:]
	for len(plain) > 0 && plain[len(plain)-1] == 0 {
		plain = plain[:len(plain)-1]
	}

	return string(plain), nil
}
