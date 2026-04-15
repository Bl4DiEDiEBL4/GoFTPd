package irc

import (
	"crypto/cipher"
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

// encryptCBC encrypts in CBC mode
func (b *BlowfishEncryptor) encryptCBC(data []byte) string {
	// Pad to block size
	blockSize := b.cipher.BlockSize()
	padLen := blockSize - (len(data) % blockSize)
	for i := 0; i < padLen; i++ {
		data = append(data, byte(padLen))
	}
	
	// Generate IV from plaintext hash (FiSH compatible)
	hash := sha1.Sum(data)
	iv := hash[:blockSize]
	
	// Create CBC encrypter
	mode := cipher.NewCBCEncrypter(b.cipher, iv)
	mode.CryptBlocks(data, data)
	
	// Base64 encode
	return base64.StdEncoding.EncodeToString(data)
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
	
	// Remove padding
	if len(data) > blockSize {
		padLen := int(data[len(data)-1])
		if padLen <= blockSize && padLen > 0 {
			data = data[:len(data)-padLen]
		}
	}
	
	return string(data[blockSize:]), nil
}
