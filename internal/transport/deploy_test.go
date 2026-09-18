package transport

import "testing"

// KindDeployNPCs — контрольное письмо разворачивания NPC-населения:
// приоритетный разбор (Regional), надёжный класс доставки.
func TestKindDeployNPCsRegistry(t *testing.T) {
	if !KindDeployNPCs.Regional() {
		t.Errorf("KindDeployNPCs.Regional() = false; want true (контрольное — приоритетный разбор)")
	}
	if got := KindDeployNPCs.Class(); got != ClassReliable {
		t.Errorf("KindDeployNPCs.Class() = %v; want %v", got, ClassReliable)
	}
}

// NPCDeployMsg — roundtrip конфига среза; битый payload — ошибка, не паника.
func TestNPCDeployMsgCodec(t *testing.T) {
	msg := NPCDeployMsg{CenterX: -71338, CenterY: 258271, Radius: 20000}
	body, err := EncodeLetter(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := DecodeLetter[NPCDeployMsg](body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back != msg {
		t.Errorf("roundtrip = %+v; want %+v", back, msg)
	}
	if _, err := DecodeLetter[NPCDeployMsg]([]byte("{")); err == nil {
		t.Errorf("битый payload разобрался; want ошибка")
	}
}
