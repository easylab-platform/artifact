package hex

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
)

// staticPrivateKey is the RSA private half of staticPublicKey. The registry
// signs names/versions protobuf envelopes with it; clients verify against the
// PEM served at /public_key.
const staticPrivateKeyPEM = `-----BEGIN PRIVATE KEY-----
MIIEvwIBADANBgkqhkiG9w0BAQEFAASCBKkwggSlAgEAAoIBAQDbbY/SxfV8B0NM
rBoIAs9zqbdf2bwuFQWhECgNaKdFSM+xnIgW0nXwQRqy7gZ46W25m8758N/mqQy1
Ga6Prfw8VcqVv9gKu11+E0Ii6n4S7BTEklUcLZSPQPjzH6JDv9BBnZx1FncBF437
dIN4WWOdkOrRDL0cQMnL8cU+b8oQZYPRHF6b0GhvC/P7rCwVQv1lvbwsGTKC0ID0
UkOLWy5BKP01qaWDER36c52f/ou5VKdEFJzxQIRfgR8ZkginKz8x48IDYN2UWmsf
yS4WqljKctM+8p1Utun94kSCdsEvBIvZLCQfyJTFQN515CzSnG1WGf+Se2lMaT/W
VuTfL0MtAgMBAAECggEAWc5NE1hG4PS+BBbZ7pZr3mxDK10bagbbj3Bj3B0NfMtQ
ieJFRoXrlCGpMjs99eWfrVwKCXyevrJIi6RPr+lm9zCrob9rRfUqTgvGwTCU2dy6
oTs8zzQOfdT7LtIvIKhULU669qbznMRNrXEhz7NSFG531Ihwq6wOi0RP1H5/Rlbr
5rcnXvz7wdCLY8UsTXzu8noz6vl4pSHxD+FnhqYwsKhG0s6c8rWLXtLoiHkuYGZ3
nIaCl26ArkBC4QSv0rx1gq/9lsU6H1YNpNxrHmP4Wl68NFoSudZYolU3m26NtLy/
JKYh18uKOYUx8E8IVQ+fviXZ0N+VXiMT8YRrMFVWRwKBgQDwNiw3HPQ6PoQhpIUz
v2eadZqtS5PltPMTNRQgavKrZGxYr4YgDMD3hu+oqpjWGDcQ2iioYnt6nxvk1Wdi
g7XtRJWxt5wSJ2bmdJgV0ur2NGEIPR1L3d9jjwhf5/BB/PIpiERDW8J4dOjOkpcc
jCWUu14dLyaGRLL95g7HSxQfWwKBgQDp2a5q57H7qhQCku6QIA3N8ahmyE2kR+Ck
q8XXugjpuMnzyIAX1sC2y2pFG3tDBElOBVfuyyVDDEIc97UJeIS8gr5faQwiS+mH
x+yQCq6L+LQWSosWVq6TbjfGfEJUp3nNOM8KCljKwzu+pvU7QvUIPekhCzEONfud
XKEKue32FwKBgQCpmoZTj5T9fuCKZIBMTkvXakwBKcjOOpoaKLMCRKD81NYPNDdu
b7Lb0qFqpLFvEP/oXTCx23810BvA0dDCZR7R3UgYh/yhcMKd2xr65cZSeh880vHZ
fFnbEMWn+brQzMkq+/S+3o4LwPgTyrr5RBbQ0g6caos36E+9J2+t1Vvq2wKBgQCt
9EbhsW7dhXwTGheqUJ3UN9KMeq3+6ZT7CehG/FVK/zIDTX+zvAVpNNHdjH7ZsGOT
TThHIwiZ4pF/mOgrnmInFJ7mvG7RSGT0o0yfLcL/zkawWk0yldKRSyjkVmTFMjvR
5FNm5aF9W1OjE/FSXxGFSwCTmw6nwpJkUZZeM0cHiwKBgQCCq18xvmSCS7YPDd6n
ZWMQlfbaS5Txo+2K0CxsGazemvsn5wmkvXQ8xvcwcG9IGP8gXZ5YaGZaEfOjZ045
XWt6u8Gq7681yCwXy2hdc+tU7pDGmYFEXQR4ACQoOV+PkB76lNL0qDxWrfpLgUZo
4Bd6OXVvjt+YrTb7CJVicJ6O5g==
-----END PRIVATE KEY-----`

var staticPrivateKey *rsa.PrivateKey

func init() {
	block, _ := pem.Decode([]byte(staticPrivateKeyPEM))
	if block == nil {
		return
	}
	if k, err := func() (*rsa.PrivateKey, error) {
		if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if rk, ok := k.(*rsa.PrivateKey); ok {
				return rk, nil
			}
		}
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}(); err == nil {
		staticPrivateKey = k
	}
}

// signPayload signs with RSA SHA-512 PKCS#1 v1.5 (mix_hex_registry verifies
// with :public_key.verify(..., :sha512, ...)).
func signPayload(payload []byte) ([]byte, error) {
	if staticPrivateKey == nil {
		return nil, errNoKey
	}
	digest := sha512.Sum512(payload)
	return rsa.SignPKCS1v15(rand.Reader, staticPrivateKey, crypto.SHA512, digest[:])
}

type hexError string

func (e hexError) Error() string { return string(e) }

const errNoKey hexError = "no signing key"
