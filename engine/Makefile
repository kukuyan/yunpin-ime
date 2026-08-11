# SPDX-License-Identifier: Apache-2.0

CXX ?= c++
CPPFLAGS ?= -Iinclude
CXXFLAGS ?= -std=c++17 -O2 -Wall -Wextra -Wpedantic -Werror
THREAD_FLAGS ?= -pthread

BUILD_DIR := build
ENGINE_SRC := src/phrase_engine.cpp src/correction_learning.cpp
TEST_BIN := $(BUILD_DIR)/phrase_engine_tests
CORRECTION_TEST_BIN := $(BUILD_DIR)/correction_learning_tests
HABIT_REPORT_BIN := $(BUILD_DIR)/yunpin_habit_report
BENCHMARK_BIN := $(BUILD_DIR)/phrase_engine_benchmark

.PHONY: all test benchmark clean

all: test benchmark

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

$(TEST_BIN): $(ENGINE_SRC) tests/test_phrase_engine.cpp include/yunpin/phrase_engine.hpp include/yunpin/correction_learning.hpp | $(BUILD_DIR)
	$(CXX) $(CPPFLAGS) $(CXXFLAGS) $(THREAD_FLAGS) $(ENGINE_SRC) tests/test_phrase_engine.cpp -o $@

$(CORRECTION_TEST_BIN): $(ENGINE_SRC) tests/test_correction_learning.cpp include/yunpin/phrase_engine.hpp include/yunpin/correction_learning.hpp | $(BUILD_DIR)
	$(CXX) $(CPPFLAGS) $(CXXFLAGS) $(THREAD_FLAGS) $(ENGINE_SRC) tests/test_correction_learning.cpp -o $@

$(HABIT_REPORT_BIN): $(ENGINE_SRC) src/habit_report_cli.cpp include/yunpin/phrase_engine.hpp include/yunpin/correction_learning.hpp | $(BUILD_DIR)
	$(CXX) $(CPPFLAGS) $(CXXFLAGS) $(THREAD_FLAGS) $(ENGINE_SRC) src/habit_report_cli.cpp -o $@

$(BENCHMARK_BIN): $(ENGINE_SRC) tests/benchmark_phrase_engine.cpp include/yunpin/phrase_engine.hpp | $(BUILD_DIR)
	$(CXX) $(CPPFLAGS) $(CXXFLAGS) $(ENGINE_SRC) tests/benchmark_phrase_engine.cpp -o $@

test: $(TEST_BIN) $(CORRECTION_TEST_BIN) $(HABIT_REPORT_BIN)
	./$(TEST_BIN)
	./$(CORRECTION_TEST_BIN)

benchmark: $(BENCHMARK_BIN)
	./$(BENCHMARK_BIN)

clean:
	rm -rf $(BUILD_DIR)
