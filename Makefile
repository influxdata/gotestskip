.PHONY: all
all: mod-download gotestskip

.PHONY: mod-download
mod-download:
	go mod download

.PHONY: gotestskip
gotestskip:
	CGO_ENABLED=0 go build -ldflags=-buildid= -o ./gotestskip ./
	strip ./gotestskip

.PHONY: clean
clean:
	rm -f ./gotestskip


