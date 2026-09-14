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
	internal/artifact) echo "internal/data internal/geo" ;;
	pkg/bufpool|pkg/mtls) echo "" ;;
	internal/protocol) echo "internal/protocol/fixture" ;;
	internal/protocol/fixture) echo "" ;;
	internal/conn) echo "internal/protocol internal/crypto" ;;
	internal/gateway) echo "internal/conn internal/protocol internal/transport internal/persist" ;;
	internal/replica) echo "internal/transport" ;;
	internal/encode) echo "internal/protocol internal/crypto pkg/bufpool" ;;
	internal/world) echo "internal/transport internal/replica internal/encode internal/data internal/geo internal/persist" ;;
	internal/party|internal/chat|internal/clan|internal/market) echo "internal/transport" ;;
	internal/persist) echo "internal/transport" ;;
	internal/loginlink|internal/admin) echo "internal/transport pkg/mtls" ;;
	internal/l2client) echo "internal/protocol internal/protocol/fixture internal/crypto internal/tap" ;;
	internal/tap) echo "internal/protocol internal/protocol/fixture internal/crypto" ;;
	internal/login) echo "internal/protocol internal/crypto internal/persist internal/loginlink internal/l2client internal/protocol/fixture internal/transport pkg/mtls" ;;
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

# Внешние модули фактического импорт-графа (go list -deps -test: только то,
# что компилируется в наши пакеты, включая тесты; полный `go list -m all`
# тянет неиспользуемые требования go.mod зависимостей) — закрытый allowlist;
# список = фактический граф gRPC-стыка (P3.4, решение 1: grpc + protobuf и
# транзитивные минимумы). Добавление новой — красный и явное решение
# (ADR/план), не молча.
allowed_mods="google.golang.org/grpc google.golang.org/protobuf google.golang.org/genproto/googleapis/rpc golang.org/x/net golang.org/x/sys golang.org/x/text"
mods_graph="$(go list -deps -test -f '{{with .Module}}{{if ne .Path "github.com/udisondev/l2go"}}{{.Path}}{{end}}{{end}}' ./... | sort -u)" ||
	{ echo "checkdeps: go list недоступен" >&2; exit 1; }
while IFS= read -r mod; do
	[ -z "$mod" ] && continue
	case " $allowed_mods " in
		*" $mod "*) ;;
		*) echo "checkdeps: внешняя зависимость вне allowlist: $mod" >&2; fail=1 ;;
	esac
done <<< "$mods_graph"

if [ "$fail" -ne 0 ]; then
	echo "checkdeps: FAIL" >&2
	exit 1
fi
echo "checkdeps: ok"
