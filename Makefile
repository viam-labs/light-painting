GO_BUILD_ENV :=
GO_BUILD_FLAGS :=
MODULE_BINARY := bin/light-painting

$(MODULE_BINARY): Makefile go.mod $(wildcard *.go) $(wildcard controller/*.go) cmd/module/*.go
	GOOS=$(VIAM_BUILD_OS) GOARCH=$(VIAM_BUILD_ARCH) $(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(MODULE_BINARY) cmd/module/main.go

lint:
	gofmt -s -w .

test:
	go test ./...

update:
	go get go.viam.com/rdk@latest
	go mod tidy

web-app-install:
	cd web && npm ci

web-app-build: web-app-install
	cd web && npm run build

module.tar.gz: meta.json $(MODULE_BINARY) web-app-build
	tar czf $@ meta.json README.md $(MODULE_BINARY) web/dist

# module.tar.gz without rebuilding the web app (web/dist must already exist).
module-fast: test $(MODULE_BINARY)
	tar czf module.tar.gz meta.json README.md $(MODULE_BINARY) web/dist

all: test module.tar.gz

clean:
	rm -rf bin module.tar.gz web/dist
