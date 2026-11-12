# RemoteAdmin — Makefile
# Стандартная сборка: make build (нужны GTK3 dev-пакеты: libgtk-3-dev)
# Полные пакеты (RPM/deb/AppImage): make packages

VERSION ?= 1.8.0
PKG     := ./cmd/remoteadmin
BIN     := remoteadmin

export GOFLAGS=-mod=vendor

.PHONY: all build test vet selftest packages rpm clean help

all: build

build: ## Сборка бинарника (нужны pkg-config, libgtk-3-dev)
	CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o $(BIN) $(PKG)

test: ## Юнит-тесты
	go test ./cmd/...

vet: ## Статический анализ
	go vet ./cmd/...

selftest: build ## Headless-самопроверка интерфейса (Xvfb)
	./$(BIN) --selftest

packages: ## Все пакеты: RPM (РЕД ОС 8), deb, AppImage, source tar.gz
	./scripts/build.sh

rpm: packages ## синоним packages

clean: ## Удалить артефакты сборки
	rm -f $(BIN) icon.png

help: ## Подсказка по целям
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  \033[36m%-12s\033[0m %s\n",$$1,$$2}'
