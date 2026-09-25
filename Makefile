# Сборка izba.
#
# CONTACT_URL обязателен для публичных сборок: сервис Fill от PaperMC
# отклоняет обобщённые User-Agent и требует ссылку для связи.

VERSION     ?= 0.1.0-dev
CONTACT_URL ?= https://github.com/TheIIIrd/mc-izba

LDFLAGS := -s -w \
	-X izba/internal/netx.Version=$(VERSION) \
	-X izba/internal/netx.ContactURL=$(CONTACT_URL)

.PHONY: build test check dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o izba ./cmd/izba

test:
	go test -race ./...

check: test
	go vet ./...
	gofmt -l . | tee /dev/stderr | (! read)

# Сборка под все целевые платформы.
dist:
	mkdir -p dist
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/izba-linux-amd64   ./cmd/izba
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/izba-linux-arm64   ./cmd/izba
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/izba-windows-amd64.exe ./cmd/izba
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/izba-darwin-arm64  ./cmd/izba
	cd dist && sha256sum * > SHA256SUMS

clean:
	rm -rf dist izba izba.exe
