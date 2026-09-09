package encode

import "testing"

func TestFrameZeroValue(t *testing.T) {
	var f Frame
	if f.ClientID != 0 || f.Payload != nil {
		t.Errorf("нулевое значение Frame не пригодно: %+v", f)
	}
}
