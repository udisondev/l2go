// Команда l2go — точка входа игрового сервера L2 Interlude.
// Сейчас тривиальна: подсистемы появятся вместе с их реализацией.
package main

import (
	"fmt"
	"os"

	"github.com/udisondev/l2go/internal/version"
)

func main() {
	fmt.Fprintf(os.Stdout, "l2go %s\n", version.String())
}
