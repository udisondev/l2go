# embedded

Каталог артефакта статики для embed-транспорта. Файл `artifact.l2a` в git не
коммитится (данные NCsoft-derived, позиция ADR-0001) и создаётся сборкой.

Цепочка release-сборки сервера одним файлом:

```
l2data build <корень датапака> <каталог геодаты> -o internal/artifact/embedded/artifact.l2a
go build -tags embedded -o bin/ ./cmd/l2go
```

или `make embedded DATA=<корень датапака> GEO=<каталог геодаты>`.

Без артефакта сборка с тегом `embedded` падает ошибкой компиляции «no matching
files» — это ожидаемо: сначала сборка артефакта, затем сборка бинараря.
