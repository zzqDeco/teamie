.PHONY: gen check-arch

gen:
	bash ops/codegen.sh

check-arch:
	bash ops/check-architecture.sh
