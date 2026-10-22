VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
export CGO_ENABLED=0

filenav:
	go build -trimpath -ldflags="$(LDFLAGS)" -o filenav .

# cross-compile for common VPS / Raspberry Pi targets (same as the release workflow)
release:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/filenav-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/filenav-linux-arm64 .
	GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/filenav-linux-armv7 .
	GOOS=linux GOARCH=386 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/filenav-linux-386 .
	cd dist && sha256sum filenav-* > SHA256SUMS

# local dev server with a throwaway config/state in ./files and ./filenav-data
ADDR ?= 127.0.0.1:8090
run: filenav
	FILENAV_PASSCODE=test1234 ./filenav serve -addr $(ADDR) -root ./files -data ./filenav-data

test:
	go vet ./... && go test ./...

# rebuild third-party browser libraries into web/vendor (needs Node)
vendor:
	cd ui-vendor && npm ci && npm run build

clean:
	rm -rf filenav dist

.PHONY: filenav release run test vendor clean
