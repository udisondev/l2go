package loginlink

import (
	"context"
	"testing"
)

type fakeClient struct{}

func (fakeClient) RegisterSession(context.Context, string, string) error { return nil }
func (fakeClient) Kick(context.Context, string) error                    { return nil }
func (fakeClient) OnlineReport(context.Context, int) error               { return nil }

func TestClientContract(t *testing.T) {
	// compile-time контракт семантики стыка (реализация — gRPC, фаза 3).
	var _ Client = fakeClient{}
}
