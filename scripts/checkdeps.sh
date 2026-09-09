#!/usr/bin/env bash
# Проверка матрицы зависимостей ADR-0005: пакетам internal/ и pkg/ разрешён
# импорт только нижележащих; пакет вне матрицы = ошибка. Часть make check.
set -euo pipefail
cd "$(dirname "$0")/.."

MODULE=github.com/udisondev/l2go

# allowed <суффикс пакета> печатает список разрешённых внутренних импортов.
# Синхронно с таблицей ADR-0005 п.5; изменение матрицы = правка ADR.
allowed() {
	case "$1" in
		internal/version|internal/crypto|internal/transport|internal/data|internal/geo) echo "" ;;
		pkg/bufpool) echo "" ;;
		internal/protocol) echo "internal/crypto pkg/bufpool" ;;
		internal/net) echo "internal/protocol internal/crypto" ;;
		internal/gateway) echo "internal/net internal/protocol internal/transport" ;;
		internal/replica) echo "internal/transport" ;;
		internal/encode) echo "internal/protocol internal/replica pkg/bufpool" ;;
		internal/world) echo "internal/transport internal/replica internal/encode internal/data internal/geo" ;;
		internal/svc) echo "internal/transport" ;;
		internal/persist) echo "internal/transport" ;;
		internal/loginlink|internal/admin) echo "" ;;
		internal/l2client) echo "internal/protocol internal/crypto" ;;
		*) echo "UNLISTED" ;;
	esac
}

fail=0
for pkg in $(go list ./... | grep -E "^$MODULE/(internal|pkg)/"); do
	suffix=${pkg#"$MODULE"/}
	allow=$(allowed "$suffix")
	if [ "$allow" = "UNLISTED" ]; then
		echo "checkdeps: пакет не в матрице ADR-0005: $suffix" >&2
		fail=1
		continue
	fi
	for dep in $(go list -deps "$pkg" | grep "^$MODULE/" | grep -v "^$pkg$" | sed "s|^$MODULE/||"); do
		case " $allow " in
			*" $dep "*) ;;
			*) echo "checkdeps: $suffix импортирует $dep — не разрешено матрицей ADR-0005" >&2; fail=1 ;;
		esac
	done
done
if [ "$fail" -ne 0 ]; then
	echo "checkdeps: FAIL" >&2
	exit 1
fi
echo "checkdeps: ok"
