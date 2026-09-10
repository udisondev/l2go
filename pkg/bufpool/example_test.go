package bufpool

import "fmt"

// Дисциплина владения: Get → использование → Put; буфер после Put не трогаем.
func Example() {
	var p Pool
	b := p.Get(300)
	copy(b, []byte("payload"))
	fmt.Println(len(b), cap(b), string(b[:7]))
	p.Put(b)
	// Output: 300 512 payload
}
