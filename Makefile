# Quality gates. `make check` is the single gate run by the pre-push hook.
GOLANGCI_VERSION := v2.13.2
GOFUMPT_VERSION  := v0.12.0
RULES_VERSION    := v1.2.3

GOBIN := $(shell go env GOPATH)/bin

.PHONY: all check fmt fmt-fix vet lint test build run tools hooks tournament e2e clean

all: check

## check: the full gate. Anything that fails here blocks a push.
check: fmt vet lint test build

## fmt: formatting must already be clean; never rewrites files in the gate.
fmt:
	@out=$$(gofmt -l . ); \
	if [ -n "$$out" ]; then \
		echo "gofmt: these files are not formatted:"; echo "$$out"; \
		echo "run 'make fmt-fix'"; exit 1; \
	fi
	@if command -v gofumpt >/dev/null 2>&1 || [ -x "$(GOBIN)/gofumpt" ]; then \
		bin=$$(command -v gofumpt || echo "$(GOBIN)/gofumpt"); \
		out=$$($$bin -l . ); \
		if [ -n "$$out" ]; then \
			echo "gofumpt: these files are not formatted:"; echo "$$out"; \
			echo "run 'make fmt-fix'"; exit 1; \
		fi; \
	else \
		echo "gofumpt not installed - run 'make tools' (gofmt check passed)"; \
	fi

## fmt-fix: rewrite files to satisfy the formatters.
fmt-fix:
	gofmt -w .
	@bin=$$(command -v gofumpt || echo "$(GOBIN)/gofumpt"); \
	if [ -x "$$bin" ]; then $$bin -w . ; else echo "gofumpt not installed - run 'make tools'"; fi

vet:
	go vet ./...

## lint: fails loudly when the linter is absent. A gate that cannot run is not a gate.
lint:
	@bin=$$(command -v golangci-lint || echo "$(GOBIN)/golangci-lint"); \
	if [ ! -x "$$bin" ]; then \
		echo "golangci-lint not installed."; \
		echo "run 'make tools' to install $(GOLANGCI_VERSION)"; \
		exit 1; \
	fi; \
	$$bin run ./...

test:
	go test -race -cover ./...

build:
	go build ./...

run:
	go run ./cmd/battlesnake

## tools: install the pinned versions the gate depends on.
tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	go install github.com/BattlesnakeOfficial/rules/cli/battlesnake@$(RULES_VERSION)

## hooks: .git/hooks is not versioned, so install our tracked hook into it.
hooks:
	install -m 0755 scripts/pre-push .git/hooks/pre-push
	@echo "installed .git/hooks/pre-push -> make check"

## tournament: batch of local games, tallied. GAMES/MODE/SEED/LABEL/JEV_ENV override.
GAMES ?= 20
MODE  ?= duel
SEED  ?= 9000
LABEL ?= run
tournament:
	scripts/tournament.sh -n $(GAMES) -m $(MODE) -s $(SEED) -l $(LABEL)

## e2e: a local game against ourselves. Requires 'make tools' and a running server.
e2e:
	@bin=$$(command -v battlesnake || echo "$(GOBIN)/battlesnake"); \
	if [ ! -x "$$bin" ]; then echo "battlesnake CLI not installed - run 'make tools'"; exit 1; fi; \
	$$bin play -W 11 -H 11 --name jev --url http://localhost:8080 -g solo -v

clean:
	go clean
	rm -f coverage.out
