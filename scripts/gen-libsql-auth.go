// gen-libsql-auth generates Ed25519 credentials for libSQL JWT auth.
//
// Usage:
//
//	go run ./scripts/gen-libsql-auth.go
//
// Outputs:
//   - SQLD_AUTH_JWT_KEY (URL-safe base64 public key for the libSQL server)
//   - LIBSQL_AUTH_TOKEN (JWT signed with the private key for clients)
//   - Optional PEM files in certificates/ if -write is set
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	write := flag.Bool("write", false, "write PEM key files to certificates/")
	expiry := flag.Duration("expiry", 10*365*24*time.Hour, "JWT validity duration")
	flag.Parse()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generating keypair: %v\n", err)
		os.Exit(1)
	}

	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	token, err := signJWT(priv, *expiry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "signing jwt: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Paste on your libSQL Railway service:")
	fmt.Printf("  SQLD_AUTH_JWT_KEY=%s\n\n", pubB64)
	fmt.Println("Paste on your kalshi-alerts app service:")
	fmt.Printf("  LIBSQL_AUTH_TOKEN=%s\n\n", token)
	fmt.Println("Alternative public key format (PKCS#8 PEM):")
	fmt.Printf("%s\n", pemPublicKey(pub))

	if *write {
		dir := "certificates"
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "creating %s: %v\n", dir, err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(dir, "libsql-jwt.key"), pemPrivateKey(priv), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "writing private key: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(dir, "libsql-jwt.pub"), pemPublicKey(pub), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "writing public key: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote %s/libsql-jwt.key and %s/libsql-jwt.pub\n", dir, dir)
	}
}

func signJWT(priv ed25519.PrivateKey, expiry time.Duration) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	payload, err := json.Marshal(map[string]any{
		"a":   "rw",
		"iat": now,
		"exp": now + int64(expiry.Seconds()),
	})
	if err != nil {
		return "", err
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(header)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := headerB64 + "." + payloadB64
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func pemPublicKey(pub ed25519.PublicKey) []byte {
	block, err := pemPublicKeyBlock(pub)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(block)
}

func pemPrivateKey(priv ed25519.PrivateKey) []byte {
	block, err := pemPrivateKeyBlock(priv)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(block)
}

func pemPublicKeyBlock(pub ed25519.PublicKey) (*pem.Block, error) {
	der, err := marshalEd25519PublicKey(pub)
	if err != nil {
		return nil, err
	}
	return &pem.Block{Type: "PUBLIC KEY", Bytes: der}, nil
}

func pemPrivateKeyBlock(priv ed25519.PrivateKey) (*pem.Block, error) {
	der, err := marshalEd25519PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return &pem.Block{Type: "PRIVATE KEY", Bytes: der}, nil
}

// PKCS#8 SubjectPublicKeyInfo for Ed25519 (OID 1.3.101.112).
func marshalEd25519PublicKey(pub ed25519.PublicKey) ([]byte, error) {
	const ed25519OID = "\x06\x03\x2b\x65\x70" // 1.3.101.112
	spki := append([]byte{
		0x30, 0x2a, // SEQUENCE (42 bytes)
		0x30, 0x05, // SEQUENCE (5 bytes) — algorithm
	}, ed25519OID...)
	spki = append(spki,
		0x03, 0x21, 0x00, // BIT STRING (33 bytes, 0 unused bits)
	)
	spki = append(spki, pub...)
	return spki, nil
}

// PKCS#8 PrivateKeyInfo for Ed25519.
func marshalEd25519PrivateKey(priv ed25519.PrivateKey) ([]byte, error) {
	seed := priv.Seed()
	const ed25519OID = "\x06\x03\x2b\x65\x70"
	// PrivateKeyInfo ::= SEQUENCE { version, algorithm, privateKey }
	inner := append([]byte{0x04, 0x22, 0x04, 0x20}, seed...) // OCTET STRING wrapping seed
	alg := append([]byte{0x30, 0x05}, ed25519OID...)
	body := append([]byte{0x02, 0x01, 0x00}, alg...) // version INTEGER 0
	body = append(body, inner...)
	der := append([]byte{0x30, byte(len(body))}, body...)
	return der, nil
}
