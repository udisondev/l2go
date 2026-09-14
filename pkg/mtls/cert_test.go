package mtls

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func parseCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("PEM не декодируется")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func TestMaterialChains(t *testing.T) {
	t.Parallel()

	m, err := GenerateMaterial("тест", []string{"gs.example"})
	if err != nil {
		t.Fatalf("GenerateMaterial: %v", err)
	}
	ca := parseCert(t, m.CA)
	if !ca.IsCA {
		t.Fatal("CA: IsCA = false")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	server := parseCert(t, m.ServerCert)
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("серверный серт не верифицируется CA (localhost): %v", err)
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, DNSName: "gs.example", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("серверный серт не верифицируется CA (доп. SAN): %v", err)
	}

	client := parseCert(t, m.ClientCert)
	if _, err := client.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("клиентский серт не верифицируется CA: %v", err)
	}

	// Чужой CA материал не подтверждает.
	foreign, err := GenerateMaterial("чужой", nil)
	if err != nil {
		t.Fatalf("GenerateMaterial(foreign): %v", err)
	}
	foreignRoots := x509.NewCertPool()
	foreignRoots.AddCert(parseCert(t, foreign.CA))
	if _, err := client.Verify(x509.VerifyOptions{Roots: foreignRoots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("клиентский серт верифицирован чужим CA: want ошибка")
	}
}

func TestWriteMaterial(t *testing.T) {
	t.Parallel()

	m, err := GenerateMaterial("тест", nil)
	if err != nil {
		t.Fatalf("GenerateMaterial: %v", err)
	}
	dir := t.TempDir()
	if err := WriteMaterial(dir, m); err != nil {
		t.Fatalf("WriteMaterial: %v", err)
	}
	if err := WriteMaterial(dir, m); err == nil {
		t.Fatal("повторная WriteMaterial: want отказ — файлы существуют")
	}
	for _, name := range []string{CAFile, ServerCertFile, ServerKeyFile, ClientCertFile, ClientKeyFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("файл %s отсутствует: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("права %s = %o; want 600", name, perm)
		}
	}
}

func TestConfigErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := ServerConfig(filepath.Join(dir, CAFile), "нет.pem", "нет.key"); err == nil {
		t.Fatal("ServerConfig без материала: want ошибка")
	}
	m, err := GenerateMaterial("тест", nil)
	if err != nil {
		t.Fatalf("GenerateMaterial: %v", err)
	}
	if err := WriteMaterial(dir, m); err != nil {
		t.Fatalf("WriteMaterial: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "битый.pem"), []byte("не PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ServerConfig(
		filepath.Join(dir, CAFile), filepath.Join(dir, "битый.pem"), filepath.Join(dir, ServerKeyFile)); err == nil {
		t.Fatal("ServerConfig с битым сертификатом: want ошибка")
	}
}

func ExampleGenerateMaterial() {
	m, err := GenerateMaterial("l2go loginlink", nil)
	if err != nil {
		return
	}
	if err := WriteMaterial("var/tls", m); err != nil {
		return
	}
	// Каталог var/tls: ca.pem, server.pem, server.key, client.pem, client.key.
}
