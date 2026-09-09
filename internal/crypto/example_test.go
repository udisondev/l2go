package crypto

import "fmt"

// Пример полного цикла login-канала: static-фаза (Init) → ключ из полей Init →
// динамическая фаза.
func ExampleLoginCrypt() {
	lc := NewLoginCrypt()

	// Первый кадр (Init): static-шифрование с фиксированным XOR-ключом.
	payload := []byte{0x00, 0x01, 0x02, 0x03}
	dst := make([]byte, len(payload)+MaxFrameOverhead) // контракт запаса
	n, err := lc.EncryptInit(dst, payload, 0x11223344)
	if err != nil {
		fmt.Println("encrypt init:", err)
		return
	}
	frame := dst[:n]

	// Приёмная сторона расшифровывает Init тем же движком, достаёт ключ из
	// полей пакета (парсер пакетов) и переводит движок в динамическую фазу.
	if err := lc.DecryptInit(frame); err != nil {
		fmt.Println("decrypt init:", err)
		return
	}
	dynamicKey := frame[:16] // в реальном пакете — поле bfKey из Init
	if err := lc.SetKey(dynamicKey); err != nil {
		fmt.Println("set key:", err)
		return
	}

	// Динамический кадр: шифрование и расшифровка.
	msg := []byte{0x05, 0x00, 0x00, 0x00}
	n, err = lc.Encrypt(dst, msg)
	if err != nil {
		fmt.Println("encrypt:", err)
		return
	}
	if err := lc.Decrypt(dst[:n]); err != nil {
		fmt.Println("decrypt:", err)
		return
	}
	fmt.Println("payload restored:", string(dst[:len(msg)]) == "\x05\x00\x00\x00")
	// Output: payload restored: true
}
