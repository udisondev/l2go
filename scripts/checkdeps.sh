#!/usr/bin/env bash
# Проверка матрицы зависимостей: пакетам internal/ и pkg/ разрешён импорт
# только ниже лежащих (включая импорты в _test.go); пакет вне матрицы = ошибка.
# Часть make check. Матрица дублирует решение о карте компонентов; меняется
# только вместе с ним.
set -euo pipefail
cd "$(dirname "$0")/.."

MODULE=github.com/udisondev/l2go

# allowed <суффикс пакета> печатает список разрешённых внутренних импортов.
allowed() {
	case "$1" in
		internal/version|internal/crypto|internal/transport|internal/data|internal/geo) echo "" ;;
		pkg/bufpool) echo "" ;;
		internal/protocol) echo "" ;;
		internal/conn) echo "internal/protocol internal/crypto" ;;
		internal/gateway) echo "internal/conn internal/protocol internal/transport" ;;
		internal/replica) echo "internal/transport" ;;
		internal/encode) echo "internal/crypto" ;;
		internal/world) echo "internal/transport internal/replica internal/encode internal/data internal/geo" ;;
		internal/service) echo "internal/transport" ;;
		internal/persist) echo "internal/transport" ;;
		internal/loginlink|internal/admin) echo "internal/transport" ;;
		internal/l2client) echo "internal/protocol internal/crypto" ;;
		internal/login) echo "internal/protocol internal/crypto" ;;
		*) echo "UNLISTED" ;;
	esac
}

# Псевдопакеты тестовых артефактов («pkg [pkg.test]», «pkg.test») отфильтрованы
# до разбиения на слова: токен с «[» в безкавычном for — glob-паттерн.
deps_of() {
	go list -deps -test "$1" |
		grep "^$MODULE/" |
		grep -v -e '\[' -e '\.test$' |
		grep -v "^$1$" |
		sed "s|^$MODULE/||"
}

fail=0
pkgs="$(go list ./... | grep -E "^$MODULE/(internal|pkg)/")"
for pkg in $pkgs; do
	suffix=${pkg#"$MODULE"/}
	allow=$(allowed "$suffix")
	if [ "$allow" = "UNLISTED" ]; then
		echo "checkdeps: пакет не в матрице: $suffix" >&2
		fail=1
		continue
	fi
	for dep in $(deps_of "$pkg"); do
		case " $allow " in
			*" $dep "*) ;;
			*) echo "checkdeps: $suffix импортирует $dep — не разрешено матрицей" >&2; fail=1 ;;
		esac
	done
done
if [ "$fail" -ne 0 ]; then
	echo "checkdeps: FAIL" >&2
	exit 1
fi
echo "checkdeps: ok"
