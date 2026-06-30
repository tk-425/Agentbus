BINARY = agentbus
INSTALL_PATH = /usr/local/bin/$(BINARY)

.PHONY: build install uninstall

build:
	go build -o $(BINARY) .

install: build
	mv $(BINARY) $(INSTALL_PATH)

uninstall:
	rm $(INSTALL_PATH)
