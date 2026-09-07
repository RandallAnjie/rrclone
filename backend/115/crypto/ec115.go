package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/big"

	"github.com/pierrec/lz4/v4"
)

// Remote P224 public key used by 115 initupload v4 (k_ec).
var remotePubKey = []byte{
	0x57, 0xA2, 0x92, 0x57, 0xCD, 0x23, 0x20, 0xE5,
	0xD6, 0xD1, 0x43, 0x32, 0x2F, 0xA4, 0xBB, 0x8A,
	0x3C, 0xF9, 0xD3, 0xCC, 0x62, 0x3E, 0xF5, 0xED,
	0xAC, 0x62, 0xB7, 0x67, 0x8A, 0x89, 0xC9, 0x1A,
	0x83, 0xBA, 0x80, 0x0D, 0x61, 0x29, 0xF5, 0x22,
	0xD0, 0x34, 0xC8, 0x95, 0xDD, 0x24, 0x65, 0x24,
	0x3A, 0xDD, 0xC2, 0x50, 0x95, 0x3B, 0xEE, 0xBA,
}

const (
	p224BaseLen = 28
	crcSalt     = "^j>WD3Kr?J2gLFjD4W2y@"
)

// EcdhCipher encrypts initupload v4 bodies and decrypts the replies.
type EcdhCipher struct {
	key    []byte
	iv     []byte
	pubKey []byte
}

// NewEcdhCipher starts a P224 ECDH session against 115's upload key.
func NewEcdhCipher() (*EcdhCipher, error) {
	//nolint:staticcheck // P224 is required by 115's initupload v4 protocol
	curve := elliptic.P224()
	priv, x, y, err := elliptic.GenerateKey(curve, rand.Reader)
	if err != nil {
		return nil, err
	}
	xBytes := make([]byte, p224BaseLen)
	x.FillBytes(xBytes)
	prefix := byte(0x02)
	if y.Bit(0) == 1 {
		prefix = 0x03
	}
	pub := append([]byte{p224BaseLen + 1, prefix}, xBytes...)

	rx := new(big.Int).SetBytes(remotePubKey[:p224BaseLen])
	ry := new(big.Int).SetBytes(remotePubKey[p224BaseLen:])
	sx, _ := curve.ScalarMult(rx, ry, priv)
	secret := sx.Bytes()
	if len(secret) < aes.BlockSize {
		return nil, fmt.Errorf("115 ecdh: short shared secret")
	}

	return &EcdhCipher{
		key:    secret[:aes.BlockSize],
		iv:     secret[len(secret)-aes.BlockSize:],
		pubKey: pub,
	}, nil
}

// Encrypt AES-CBC encrypts plaintext for initupload.php.
func (c *EcdhCipher) Encrypt(plainText []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	data := pkcs7Pad(plainText, aes.BlockSize)
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, c.iv).CryptBlocks(out, data)
	return out, nil
}

// Decrypt AES-CBC decrypts and LZ4-decompresses an initupload reply.
func (c *EcdhCipher) Decrypt(cipherText []byte) (text []byte, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("115 ecdh decrypt: %v", rec)
		}
	}()
	if len(cipherText) < aes.BlockSize {
		return nil, fmt.Errorf("115 ecdh: short ciphertext")
	}
	cipherText = cipherText[:len(cipherText)-len(cipherText)%aes.BlockSize]
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(cipherText))
	cipher.NewCBCDecrypter(block, c.iv).CryptBlocks(plain, cipherText)
	if len(plain) < 2 {
		return nil, fmt.Errorf("115 ecdh: short plaintext")
	}
	length := int(plain[0]) + int(plain[1])<<8
	src := plain[2:]
	if length < 0 || length > len(src) {
		return nil, fmt.Errorf("115 ecdh: invalid lz4 length %d", length)
	}
	dst := make([]byte, 0x10000)
	n, err := lz4.UncompressBlock(src[:length], dst)
	if err != nil {
		return nil, err
	}
	return dst[:n], nil
}

// EncodeToken builds the k_ec query token for initupload v4.
func (c *EcdhCipher) EncodeToken(timestamp int64) (string, error) {
	r1b, err := rand.Int(rand.Reader, big.NewInt(256))
	if err != nil {
		return "", err
	}
	r2b, err := rand.Int(rand.Reader, big.NewInt(256))
	if err != nil {
		return "", err
	}
	r1 := byte(r1b.Uint64())
	r2 := byte(r2b.Uint64())

	tmp := make([]byte, 0, 48)
	timeBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(timeBytes, uint32(timestamp))

	for i := 0; i < 15; i++ {
		tmp = append(tmp, c.pubKey[i]^r1)
	}
	tmp = append(tmp, r1, 0x73^r1)
	for i := 0; i < 3; i++ {
		tmp = append(tmp, r1)
	}
	for i := 0; i < 4; i++ {
		tmp = append(tmp, r1^timeBytes[3-i])
	}
	for i := 15; i < len(c.pubKey); i++ {
		tmp = append(tmp, c.pubKey[i]^r2)
	}
	tmp = append(tmp, r2, 0x01^r2)
	for i := 0; i < 3; i++ {
		tmp = append(tmp, r2)
	}

	crc := crc32.ChecksumIEEE(append([]byte(crcSalt), tmp...))
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	for i := 0; i < 4; i++ {
		tmp = append(tmp, crcBytes[3-i])
	}
	return base64.StdEncoding.EncodeToString(tmp), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}
