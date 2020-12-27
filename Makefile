all: build

install: build
	go install

build:
	go build -o dist/squashfs-util .
