package desktop

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// A persistent certificate is authenticated by the QR's SHA-256 pin, rather
// than a public CA. Expiry or a changed certificate requires scanning a new QR.
func localCertificate(dir string) (tls.Certificate, string, error) {
	certPath, keyPath := filepath.Join(dir, "lan.crt"), filepath.Join(dir, "lan.key")
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return tls.Certificate{}, "", err
		}
		tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Blooper local device"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(5, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}}
		der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
			return tls.Certificate{}, "", err
		}
		if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return cert, "", err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return cert, "", err
	}
	if time.Now().After(leaf.NotAfter) {
		return cert, "", fmt.Errorf("local certificate expired; remove lan.crt/lan.key and pair devices again")
	}
	digest := sha256.Sum256(cert.Certificate[0])
	return cert, hex.EncodeToString(digest[:]), nil
}

func lanAddresses(port int) []string {
	var urls []string
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil && ip.IsPrivate() {
				urls = append(urls, fmt.Sprintf("https://%s:%d", ip, port))
			}
		}
	}
	return urls
}
