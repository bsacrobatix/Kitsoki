BINARY    := kitsoki
PKG       := ./cmd/kitsoki
INSTALLDIR ?= $(HOME)/bin

.PHONY: all build install uninstall test vet fmt tidy clean

all: build

build:
	go build -o $(BINARY) $(PKG)

install:
	@mkdir -p $(INSTALLDIR)
	GOBIN=$(INSTALLDIR) go install $(PKG)
	@echo "installed $(BINARY) -> $(INSTALLDIR)/$(BINARY)"

uninstall:
	rm -f $(INSTALLDIR)/$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
