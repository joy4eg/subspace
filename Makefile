BUILD_VERSION ?=unknown

build:
	CGO_ENABLED=0 \
		go build -v --compiler gc --ldflags "-extldflags -static -s -w -X main.version=${BUILD_VERSION}" -o subspace ./cmd/subspace

.PHONY: clean
clean:
	rm -f subspace
