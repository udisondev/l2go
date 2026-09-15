#!/usr/bin/env bash
# Локальный -race без системного gcc: пользовательский musl-cross-тулчейн
# (gcc 11.2). Установка (однократно):
#   wget -qO- https://musl.cc/x86_64-linux-musl-cross.tgz | tar xz -C ~/.local/toolchains --strip-components=0
#     (каталог x86_64-linux-musl-cross появится внутри ~/.local/toolchains)
# Штатный альтернативный путь: sudo apt-get install -y gcc && CGO_ENABLED=1 go test -race ./...
set -euo pipefail
cd "$(dirname "$0")/.."

TC="$HOME/.local/toolchains/x86_64-linux-musl-cross"
if [ ! -x "$TC/bin/x86_64-linux-musl-gcc" ]; then
	echo "race: тулчейн не найден в $TC — см. комментарий в шапке скрипта" >&2
	exit 1
fi
CGO_ENABLED=1 \
	CC="$TC/bin/x86_64-linux-musl-gcc -Wl,-dynamic-linker,$TC/x86_64-linux-musl/lib/libc.so" \
	go test -race ./... "$@"
