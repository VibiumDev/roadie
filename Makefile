BINARY = roadie

.PHONY: build test clean

build:
	go build -o $(BINARY) .

test:
	go test -race -v ./...

clean:
	rm -f $(BINARY)
