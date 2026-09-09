GO ?= go
# Пин по версии модуля: 2025.1 не читает export data Go 1.27.
STATICCHECK_VERSION ?= v0.8.1

# check — единственная команда для агентов и CI: все ворота разом
# (build + vet + staticcheck + test -race + матрица зависимостей).
.PHONY: build test race lint checkdeps check tidy

build:
	$(GO) build ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=1 ./...

lint:
	$(GO) vet ./...
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

checkdeps:
	bash scripts/checkdeps.sh

tidy:
	$(GO) mod tidy

check: build lint checkdeps race
