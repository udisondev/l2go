package data_test

import (
	"fmt"
	"os"

	"github.com/udisondev/l2go/internal/data"
)

func ExampleLoad() {
	_, rep, err := data.Load(os.DirFS("testdata/synth"))
	if err != nil {
		fmt.Println("фатальная ошибка FS:", err)
		return
	}
	fmt.Println("предметов:", rep.Items, "ошибки:", rep.HasErrors())
	// Output:
	// предметов: 5 ошибки: false
}
