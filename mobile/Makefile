# SPDX-License-Identifier: Apache-2.0

BUILD_DIR ?= build

.PHONY: test clean

test:
	cmake -S . -B $(BUILD_DIR) -DCMAKE_BUILD_TYPE=Release
	cmake --build $(BUILD_DIR) --parallel
	ctest --test-dir $(BUILD_DIR) --output-on-failure

clean:
	cmake -E remove_directory $(BUILD_DIR)
