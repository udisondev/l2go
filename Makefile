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

FUZZTIME ?= 10m

tidy:
	$(GO) mod tidy

check: build lint checkdeps race

# Fuzz-смоук: короткий прогон всех целей (CI гоняет тот же список).
fuzz-smoke:
	$(GO) test -fuzz='^FuzzRoundtripFixed$$' -fuzztime=10s ./internal/protocol/
	$(GO) test -fuzz='^FuzzRoundtripS$$' -fuzztime=10s ./internal/protocol/
	$(GO) test -fuzz='^FuzzReadOffsets$$' -fuzztime=10s ./internal/protocol/
	$(GO) test -fuzz='^FuzzNextFrame$$' -fuzztime=10s ./internal/protocol/
	$(GO) test -fuzz='^FuzzLoginDecrypt$$' -fuzztime=10s ./internal/crypto/
	$(GO) test -fuzz='^FuzzGameDecrypt$$' -fuzztime=10s ./internal/crypto/
	$(GO) test -fuzz='^FuzzLoadItems$$' -fuzztime=10s ./internal/data/

# Длинный локальный фаззинг: make fuzz-long FUZZTIME=30m (находки — в testdata/fuzz).
fuzz-long:
	$(GO) test -fuzz='^FuzzRoundtripFixed$$' -fuzztime=$(FUZZTIME) ./internal/protocol/
	$(GO) test -fuzz='^FuzzRoundtripS$$' -fuzztime=$(FUZZTIME) ./internal/protocol/
	$(GO) test -fuzz='^FuzzReadOffsets$$' -fuzztime=$(FUZZTIME) ./internal/protocol/
	$(GO) test -fuzz='^FuzzNextFrame$$' -fuzztime=$(FUZZTIME) ./internal/protocol/
	$(GO) test -fuzz='^FuzzLoginDecrypt$$' -fuzztime=$(FUZZTIME) ./internal/crypto/
	$(GO) test -fuzz='^FuzzGameDecrypt$$' -fuzztime=$(FUZZTIME) ./internal/crypto/
	$(GO) test -fuzz='^FuzzLoadItems$$' -fuzztime=$(FUZZTIME) ./internal/data/
