package loginlink

import (
	"context"
	"testing"
)

// mTLS стыка (требование владельца): левый процесс не регистрируется и не
// выдаёт себя за LS.

// rpcRejected — прямая попытка регистрации отклонена транспортом.
func rpcRejected(t *testing.T, c *Client) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*ValidateTimeout)
	defer cancel()
	stream, err := c.stub.RegisterGameServer(ctx, &RegisterGameServerRequest{HexId: []byte("hex-x")})
	if err != nil {
		return true // handshake/аутентификация не прошла
	}
	if _, err = stream.Recv(); err != nil {
		return true
	}
	return false
}

func TestMTLSRejectClientWithoutCert(t *testing.T) {
	stack := newTestMaterial(t)
	_, _, addr := startLink(t, stack)
	// Клиент доверяет CA, но клиентской пары не предъявляет.
	anonymous := stack.client.Clone()
	anonymous.Certificates = nil
	c, err := Dial(ClientConfig{Addr: addr, HexID: []byte("hex-a"), TLS: anonymous})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if !rpcRejected(t, c) {
		t.Fatal("регистрация без клиентского сертификата: want отказ")
	}
}

func TestMTLSRejectForeignCA(t *testing.T) {
	stack := newTestMaterial(t)
	_, _, addr := startLink(t, stack)
	foreign := newTestMaterial(t) // другой CA
	c, err := Dial(ClientConfig{Addr: addr, HexID: []byte("hex-a"), TLS: foreign.client})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if !rpcRejected(t, c) {
		t.Fatal("регистрация с сертификатом чужого CA: want отказ")
	}
}

func TestMTLSRejectForeignServer(t *testing.T) {
	// Левый LS (серверный сертификат не от общего CA) — клиент отказывается
	// соединяться: impersonate невозможен.
	stack := newTestMaterial(t)
	foreign := newTestMaterial(t)
	_, _, addr := startLink(t, foreign)
	c, err := Dial(ClientConfig{Addr: addr, HexID: []byte("hex-a"), TLS: stack.client})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if !rpcRejected(t, c) {
		t.Fatal("соединение с левым серверным сертификатом: want отказ")
	}
}
