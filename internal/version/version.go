// Package version хранит версию сборки.
package version

// Version подменяется при релизных сборках через -ldflags "-X ...version.Version=v1.2.3".
var Version = "dev"

// String возвращает текущую версию сборки.
func String() string { return Version }
