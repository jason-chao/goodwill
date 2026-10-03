VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GEO     := internal/geo/data/country.mmdb.gz

.PHONY: build build-nogeo geo geo-update test check tracker clean

# A static binary with the country database built in.
build: $(GEO)
	CGO_ENABLED=0 go build -trimpath -tags embedgeo -ldflags "$(LDFLAGS)" -o goodwill ./cmd/goodwill

# The same without a country database; set geo.path to use your own.
build-nogeo:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o goodwill ./cmd/goodwill

geo: $(GEO)

$(GEO):
	./scripts/fetch-geo.sh

geo-update:
	./scripts/fetch-geo.sh

test:
	go test ./...

# Everything CI runs.
check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	go test -race ./...

# Rebuild tracker/script.js from the pinned upstream release.
tracker:
	./scripts/build-tracker.sh

clean:
	rm -f goodwill
	rm -rf dist
