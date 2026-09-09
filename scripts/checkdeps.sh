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
		internal/encode) echo "internal/protocol internal/crypto pkg/bufpool" ;;
		internal/world) echo "internal/transport internal/replica internal/encode internal/data internal/geo" ;;
		internal/party|internal/chat|internal/clan|internal/market) echo "internal/transport" ;;
		internal/persist) echo "internal/transport" ;;
		internal/loginlink|internal/admin) echo "internal/transport" ;;
		internal/l2client) echo "internal/protocol internal/crypto" ;;
		internal/login) echo "internal/protocol internal/crypto" ;;
		*) echo "UNLISTED" ;;
	esac
}

# deps_of — чистый фильтр: его статус отбрасывается вызовом в списке for
# (подстановка в words-позиции не проверяется errexit). Закономерное
# опустошение: на пакете без внутренних депов последний grep -v "^$pkg$"
# даёт rc=1 и под pipefail роняет пайплайн — это ожидаемо. Фатальная
# проверка go list живёт в теле цикла ниже.
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
	# Фатальная проба: errexit действует только в теле цикла, не в подстановке.
	go list -deps -test "$pkg" >/dev/null
	for dep in $(deps_of "$pkg"); do
		case " $allow " in
			*" $dep "*) ;;
			*) echo "checkdeps: $suffix импортирует $dep — не разрешено матрицей" >&2; fail=1 ;;
		esac
	done
done

# Внешние модули графа (весь `go list -m all`: прямые и косвенные) — закрытый
# allowlist. golang.org/x/crypto — единственная прямая зависимость; последние
# четыре — её собственный граф (рост графа = сознательная правка списка).
# Расширение списка — вместе с зафиксированным решением (ADR/план), не молча.
mods="$(go list -m all)" || { echo "checkdeps: go list -m all недоступен" >&2; exit 1; }
while IFS= read -r dep; do
	[ -z "$dep" ] && continue
	case " golang.org/x/crypto golang.org/x/net golang.org/x/sys golang.org/x/term golang.org/x/text " in
		*" $dep "*) ;;
		*) echo "checkdeps: внешняя зависимость вне allowlist: $dep" >&2; fail=1 ;;
	esac
done < <(printf '%s\n' "$mods" | tail -n +2 | grep -v "^$MODULE$" | awk '{print $1}')

if [ "$fail" -ne 0 ]; then
	echo "checkdeps: FAIL" >&2
	exit 1
fi
echo "checkdeps: ok"
