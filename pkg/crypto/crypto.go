package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

func md5Hex(s string) string {
	h := md5.New()
	_, _ = h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// VerifySignature checks signature by sorting and md5 hashing provided fields
func VerifySignature(appKey, token, timestamp, nonce, encodingContent, signature string) (bool, string) {
	parts := []string{appKey, token, timestamp, nonce, encodingContent}
	sort.Strings(parts)
	joined := strings.Join(parts, "")
	calc := md5Hex(joined)
	return strings.EqualFold(calc, signature), calc
}

func pkcs5Pad(src []byte, blockSize int) []byte {
	pad := blockSize - len(src)%blockSize
	padding := bytesRepeat(byte(pad), pad)
	return append(src, padding...)
}

func pkcs5Unpad(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, errors.New("empty plaintext")
	}
	pad := int(src[len(src)-1])
	if pad == 0 || pad > len(src) {
		return nil, errors.New("invalid padding")
	}
	for i := 0; i < pad; i++ {
		if src[len(src)-1-i] != byte(pad) {
			return nil, errors.New("invalid padding")
		}
	}
	return src[:len(src)-pad], nil
}

// AESCBCEncryptBase64 encrypts plain text with key using AES/CBC/PKCS5 then base64 encodes
func AESCBCEncryptBase64(plain, key string) (string, error) {
	k := []byte(key)
	if n := len(k); !(n == 16 || n == 24 || n == 32) {
		return "", errors.New("AES key length must be 16/24/32 bytes")
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	pt := pkcs5Pad([]byte(plain), bs)
	iv := k[:bs]
	mode := cipher.NewCBCEncrypter(block, iv)
	ct := make([]byte, len(pt))
	mode.CryptBlocks(ct, pt)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// AESCBCDecryptBase64 decodes base64 cipher then decrypts using AES/CBC/PKCS5
func AESCBCDecryptBase64(b64, key string) (string, error) {
	ct, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	k := []byte(key)
	if n := len(k); !(n == 16 || n == 24 || n == 32) {
		return "", errors.New("AES key length must be 16/24/32 bytes")
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	if len(ct)%bs != 0 {
		return "", errors.New("ciphertext is not a multiple of block size")
	}
	iv := k[:bs]
	mode := cipher.NewCBCDecrypter(block, iv)
	pt := make([]byte, len(ct))
	mode.CryptBlocks(pt, ct)
	unpadded, err := pkcs5Unpad(pt)
	if err != nil {
		return "", err
	}
	return string(unpadded), nil
}

func bytesRepeat(b byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = b
	}
	return out
}
