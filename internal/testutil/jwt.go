package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"sync"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

var jwtFixture struct {
	sync.Once
	pem string
	err error
}

func JWTConfig() (config.JWT, error) {
	jwtFixture.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			jwtFixture.err = err
			return
		}
		jwtFixture.pem = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	})
	return config.JWT{Issuer: "test", Audience: "test", Algorithm: "RS256", KeyID: "test-key", PrivateKey: jwtFixture.pem, TTL: time.Hour}, jwtFixture.err
}
