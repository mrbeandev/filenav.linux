VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
export CGO_ENABLED=0

filenav:
	go build -trimpath -ldflags="$(LDFLAGS)" -o filenav .

# cross-compile for common small VPS / Raspberry Pi targets
release:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/filenav-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/filenav-linux-arm64 .
	GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/filenav-linux-armv7 .

run: filenav
	FILENAV_PASSCODE=test1234 ./filenav -root ./files -data ./filenav-data

clean:
	rm -rf filenav dist

.PHONY: filenav release run clean
