package crypto

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
)

// Protocol constants used by 115's m115 (chrome downurl) encoding.
// These are public key material and XOR tables from the web/app client.
var (
	xorKeySeed = []byte{
		0xf0, 0xe5, 0x69, 0xae, 0xbf, 0xdc, 0xbf, 0x8a,
		0x1a, 0x45, 0xe8, 0xbe, 0x7d, 0xa6, 0x73, 0xb8,
		0xde, 0x8f, 0xe7, 0xc4, 0x45, 0xda, 0x86, 0xc4,
		0x9b, 0x64, 0x8b, 0x14, 0x6a, 0xb4, 0xf1, 0xaa,
		0x38, 0x01, 0x35, 0x9e, 0x26, 0x69, 0x2c, 0x86,
		0x00, 0x6b, 0x4f, 0xa5, 0x36, 0x34, 0x62, 0xa6,
		0x2a, 0x96, 0x68, 0x18, 0xf2, 0x4a, 0xfd, 0xbd,
		0x6b, 0x97, 0x8f, 0x4d, 0x8f, 0x89, 0x13, 0xb7,
		0x6c, 0x8e, 0x93, 0xed, 0x0e, 0x0d, 0x48, 0x3e,
		0xd7, 0x2f, 0x88, 0xd8, 0xfe, 0xfe, 0x7e, 0x86,
		0x50, 0x95, 0x4f, 0xd1, 0xeb, 0x83, 0x26, 0x34,
		0xdb, 0x66, 0x7b, 0x9c, 0x7e, 0x9d, 0x7a, 0x81,
		0x32, 0xea, 0xb6, 0x33, 0xde, 0x3a, 0xa9, 0x59,
		0x34, 0x66, 0x3b, 0xaa, 0xba, 0x81, 0x60, 0x48,
		0xb9, 0xd5, 0x81, 0x9c, 0xf8, 0x6c, 0x84, 0x77,
		0xff, 0x54, 0x78, 0x26, 0x5f, 0xbe, 0xe8, 0x1e,
		0x36, 0x9f, 0x34, 0x80, 0x5c, 0x45, 0x2c, 0x9b,
		0x76, 0xd5, 0x1b, 0x8f, 0xcc, 0xc3, 0xb8, 0xf5,
	}

	xorClientKey = []byte{
		0x78, 0x06, 0xad, 0x4c, 0x33, 0x86, 0x5d, 0x18,
		0x4c, 0x01, 0x3f, 0x46,
	}

	rsaPublicKey = []byte("-----BEGIN PUBLIC KEY-----\n" +
		"MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCGhpgMD1okxLnUMCDNLCJwP/P0\n" +
		"UHVlKQWLHPiPCbhgITZHcZim4mgxSWWb0SLDNZL9ta1HlErR6k02xrFyqtYzjDu2\n" +
		"rGInUC0BCZOsln0a7wDwyOA43i5NO8LsNory6fEKbx7aT3Ji8TZCDAfDMbhxvxOf\n" +
		"dPMBDjxP5X3zr7cWgwIDAQAB\n" +
		"-----END PUBLIC KEY-----")

	rsaServerKey *rsa.PublicKey
)

// Key is a 16-byte m115 session key.
type Key [16]byte

func init() {
	block, _ := pem.Decode(rsaPublicKey)
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(err)
	}
	rsaServerKey = key.(*rsa.PublicKey)
}

// GenerateKey returns a random m115 key.
func GenerateKey() Key {
	var key Key
	_, _ = io.ReadFull(rand.Reader, key[:])
	return key
}

// Encode packs input with key using the m115 scheme used by proapi downurl.
func Encode(input []byte, key Key) string {
	buf := make([]byte, 16+len(input))
	copy(buf, key[:])
	copy(buf[16:], input)
	xorTransform(buf[16:], xorDeriveKey(key[:], 4))
	reverseBytes(buf[16:])
	xorTransform(buf[16:], xorClientKey)
	return base64.StdEncoding.EncodeToString(rsaEncrypt(buf))
}

// Decode unpacks an m115 payload using the same key that Encode used.
func Decode(input string, key Key) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		return nil, err
	}
	data = rsaDecrypt(data)
	if len(data) < 16 {
		return nil, io.ErrUnexpectedEOF
	}
	output := make([]byte, len(data)-16)
	copy(output, data[16:])
	xorTransform(output, xorDeriveKey(data[:16], 12))
	reverseBytes(output)
	xorTransform(output, xorDeriveKey(key[:], 4))
	return output, nil
}

func xorDeriveKey(seed []byte, size int) []byte {
	key := make([]byte, size)
	for i := 0; i < size; i++ {
		key[i] = (seed[i] + xorKeySeed[size*i]) & 0xff
		key[i] ^= xorKeySeed[size*(size-i-1)]
	}
	return key
}

func xorTransform(data, key []byte) {
	dataSize, keySize := len(data), len(key)
	if keySize == 0 {
		return
	}
	mod := dataSize % 4
	if mod > 0 {
		for i := 0; i < mod; i++ {
			data[i] ^= key[i%keySize]
		}
	}
	for i := mod; i < dataSize; i++ {
		data[i] ^= key[(i-mod)%keySize]
	}
}

func rsaEncrypt(input []byte) []byte {
	plainSize, blockSize := len(input), rsaServerKey.Size()-11
	var buf bytes.Buffer
	for offset := 0; offset < plainSize; offset += blockSize {
		sliceSize := blockSize
		if offset+sliceSize > plainSize {
			sliceSize = plainSize - offset
		}
		slice, _ := rsa.EncryptPKCS1v15(rand.Reader, rsaServerKey, input[offset:offset+sliceSize])
		buf.Write(slice)
	}
	return buf.Bytes()
}

func rsaDecrypt(input []byte) []byte {
	output := make([]byte, 0, len(input))
	cipherSize, blockSize := len(input), rsaServerKey.Size()
	for offset := 0; offset < cipherSize; offset += blockSize {
		sliceSize := blockSize
		if offset+sliceSize > cipherSize {
			sliceSize = cipherSize - offset
		}
		n := new(big.Int).SetBytes(input[offset : offset+sliceSize])
		m := new(big.Int).Exp(n, big.NewInt(int64(rsaServerKey.E)), rsaServerKey.N)
		b := m.Bytes()
		index := bytes.IndexByte(b, 0x00)
		if index < 0 {
			return nil
		}
		output = append(output, b[index+1:]...)
	}
	return output
}

func reverseBytes(data []byte) {
	for i, j := 0, len(data)-1; i < j; i, j = i+1, j-1 {
		data[i], data[j] = data[j], data[i]
	}
}
