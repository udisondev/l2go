// Пакет mtls — материалы взаимной TLS-аутентификации: самоподписанный CA,
// серверная и клиентская пары сертификатов (ed25519), сборка tls.Config для
// gRPC/транспортов с проверкой обеих сторон. Только stdlib; полезен любому
// сервису с внутренним стыком взаимного доверия.
package mtls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Имена файлов материала в каталоге.
const (
	CAFile         = "ca.pem"
	ServerCertFile = "server.pem"
	ServerKeyFile  = "server.key"
	ClientCertFile = "client.pem"
	ClientKeyFile  = "client.key"
)

// validityPeriod — срок сертификатов: внутренняя инфраструктура одного
// владельца, ротация — регенерацией материала.
const validityPeriod = 10 * 365 * 24 * time.Hour

// Material — сгенерированный PEM-материал: CA, серверная и клиентская пары.
type Material struct {
	CA         []byte
	ServerCert []byte
	ServerKey  []byte
	ClientCert []byte
	ClientKey  []byte
}

// GenerateMaterial выписывает CA, серверную пару (SAN: localhost, 127.0.0.1,
// ::1 и serverNames) и клиентскую пару. cn — префикс CommonName субъектов
// (например "l2go loginlink"): «<cn> CA», «<cn> server», «<cn> client».
func GenerateMaterial(cn string, serverNames []string) (*Material, error) {
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("mtls: CA-ключ: %w", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: cn + " CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validityPeriod),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caKey.Public(), caKey)
	if err != nil {
		return nil, fmt.Errorf("mtls: CA-сертификат: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("mtls: CA-парсинг: %w", err)
	}

	serverCert, serverKey, err := issuePair(caCert, caKey, &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: cn + " server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(validityPeriod),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     append([]string{"localhost"}, serverNames...),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	})
	if err != nil {
		return nil, err
	}
	clientCert, clientKey, err := issuePair(caCert, caKey, &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: cn + " client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(validityPeriod),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, err
	}
	return &Material{
		CA:         pemEncode("CERTIFICATE", caDER),
		ServerCert: serverCert,
		ServerKey:  serverKey,
		ClientCert: clientCert,
		ClientKey:  clientKey,
	}, nil
}

func issuePair(ca *x509.Certificate, caKey ed25519.PrivateKey,
	tmpl *x509.Certificate) (certPEM, keyPEM []byte, err error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: ключ пары: %w", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: сертификат %s: %w", tmpl.Subject.CommonName, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: ключ %s: %w", tmpl.Subject.CommonName, err)
	}
	return pemEncode("CERTIFICATE", der), pemEncode("PRIVATE KEY", keyDER), nil
}

func pemEncode(block string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: block, Bytes: der})
}

func randomSerial() *big.Int {
	// Позитивный 128-битный серийник; ошибка rand невозможна на практике —
	// ключи этим же rand уже сгенерированы.
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(fmt.Sprintf("mtls: серийник: %v", err))
	}
	return n
}

// WriteMaterial раскладывает материал по каталогу (0700/0600); существующие
// файлы не перезаписываются — ротация осознанным удалением каталога.
func WriteMaterial(dir string, m *Material) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mtls: каталог %s: %w", dir, err)
	}
	files := []struct {
		name string
		data []byte
	}{
		{CAFile, m.CA},
		{ServerCertFile, m.ServerCert},
		{ServerKeyFile, m.ServerKey},
		{ClientCertFile, m.ClientCert},
		{ClientKeyFile, m.ClientKey},
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("mtls: %s уже существует — ротация удалением каталога", path)
		}
		if err := os.WriteFile(path, f.data, 0o600); err != nil {
			return fmt.Errorf("mtls: запись %s: %w", path, err)
		}
	}
	return nil
}

// ServerConfig собирает tls.Config сервера: клиентский сертификат обязателен
// и проверяется против CA (взаимная аутентификация).
func ServerConfig(caPath, certPath, keyPath string) (*tls.Config, error) {
	pool, err := loadCAPool(caPath)
	if err != nil {
		return nil, err
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("mtls: серверная пара %s/%s: %w", certPath, keyPath, err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ClientConfig собирает tls.Config клиента: сервер проверяется против CA по
// имени serverName (левый сервер с чужим сертификатом не пройдёт).
func ClientConfig(caPath, certPath, keyPath, serverName string) (*tls.Config, error) {
	pool, err := loadCAPool(caPath)
	if err != nil {
		return nil, err
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("mtls: клиентская пара %s/%s: %w", certPath, keyPath, err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func loadCAPool(caPath string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("mtls: CA %s: %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("mtls: CA %s: нет сертификатов в PEM", caPath)
	}
	return pool, nil
}
