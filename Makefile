GO ?= go
# Пин по версии модуля: 2025.1 не читает export data Go 1.27.
STATICCHECK_VERSION ?= v0.8.1

# check — единственная команда для агентов и CI: все ворота разом
# (build + lint: vet, staticcheck, gofmt + checkdeps + test -race).
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
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt required:"; echo "$$out"; exit 1; fi

checkdeps:
	bash scripts/checkdeps.sh

tidy:
	$(GO) mod tidy

check: build lint checkdeps race
