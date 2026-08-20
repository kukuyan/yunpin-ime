# SPDX-License-Identifier: Apache-2.0

BUILD_DIR ?= build
GO ?= go
PYTHON ?= python3

.PHONY: test test-candidate-core test-sync-core test-contract clean

test: test-candidate-core test-sync-core test-contract

test-candidate-core:
	cmake -S . -B $(BUILD_DIR) -DCMAKE_BUILD_TYPE=Release
	cmake --build $(BUILD_DIR) --parallel
	ctest --test-dir $(BUILD_DIR) --output-on-failure

test-sync-core:
	cd synccore && $(GO) test ./...

test-contract:
	cd .. && $(PYTHON) -m unittest tests.test_mobile_contract

clean:
	cmake -E remove_directory $(BUILD_DIR)
