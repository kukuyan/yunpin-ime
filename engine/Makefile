# SPDX-License-Identifier: Apache-2.0

CXX ?= c++
CPPFLAGS ?= -Iinclude
CXXFLAGS ?= -std=c++17 -O2 -Wall -Wextra -Wpedantic -Werror
THREAD_FLAGS ?= -pthread

BUILD_DIR := build
ENGINE_SRC := src/phrase_engine.cpp
TEST_BIN := $(BUILD_DIR)/phrase_engine_tests
BENCHMARK_BIN := $(BUILD_DIR)/phrase_engine_benchmark

.PHONY: all test benchmark clean

all: test benchmark

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

$(TEST_BIN): $(ENGINE_SRC) tests/test_phrase_engine.cpp include/yunpin/phrase_engine.hpp | $(BUILD_DIR)
	$(CXX) $(CPPFLAGS) $(CXXFLAGS) $(THREAD_FLAGS) $(ENGINE_SRC) tests/test_phrase_engine.cpp -o $@

$(BENCHMARK_BIN): $(ENGINE_SRC) tests/benchmark_phrase_engine.cpp include/yunpin/phrase_engine.hpp | $(BUILD_DIR)
	$(CXX) $(CPPFLAGS) $(CXXFLAGS) $(ENGINE_SRC) tests/benchmark_phrase_engine.cpp -o $@

test: $(TEST_BIN)
	./$(TEST_BIN)

benchmark: $(BENCHMARK_BIN)
	./$(BENCHMARK_BIN)

clean:
	rm -rf $(BUILD_DIR)
