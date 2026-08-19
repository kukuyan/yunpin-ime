.PHONY: check test test-engine test-mobile test-tools test-localstore-docker test-protocol-docker test-sync test-sync-docker test-integration-docker privacy-check license-check supply-chain-check interface-check

check: test test-protocol-docker test-localstore-docker test-sync-docker test-integration-docker privacy-check license-check supply-chain-check interface-check

test: test-engine test-mobile test-tools

test-engine:
	$(MAKE) -C engine test benchmark

test-mobile:
	$(MAKE) -C mobile test

test-tools:
	cd tools/importer && PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -p 'test_*.py' -v
	cd tools/public_pack && PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -p 'test_*.py' -v

test-protocol-docker:
	docker build --target test -t yunpin-protocol:test ./protocol

test-localstore-docker:
	docker build --target test -f localstore/Dockerfile -t yunpin-localstore:test .

test-sync:
	cd sync && go test ./...

test-sync-docker:
	docker build --target test -t yunpin-sync:test ./sync

test-integration-docker:
	docker build --target test -f integration/Dockerfile -t yunpin-integration:test .

privacy-check:
	bash scripts/check_private_data.sh

license-check:
	python3 scripts/check_licenses.py
	python3 scripts/check_submodule_locks.py

supply-chain-check:
	python3 scripts/check_supply_chain.py

interface-check:
	ruby scripts/check_yaml.rb
	docker compose config --quiet
