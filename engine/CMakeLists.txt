# SPDX-License-Identifier: Apache-2.0
cmake_minimum_required(VERSION 3.20)
project(yunpin_phrase_engine LANGUAGES CXX)

find_package(Threads REQUIRED)

if(MSVC)
  # Source and test fixtures contain Chinese UTF-8 text.  Do not let a
  # machine's active Windows code page change how MSVC tokenizes the files.
  add_compile_options(/utf-8)
endif()

add_library(yunpin_phrase_engine STATIC
  src/phrase_engine.cpp
  src/correction_learning.cpp
)
target_include_directories(yunpin_phrase_engine PUBLIC include)
target_compile_features(yunpin_phrase_engine PUBLIC cxx_std_17)

if(MSVC)
  target_compile_options(yunpin_phrase_engine PRIVATE /W4 /WX /permissive-)
else()
  target_compile_options(yunpin_phrase_engine PRIVATE -Wall -Wextra -Wpedantic -Werror)
endif()

enable_testing()
add_executable(phrase_engine_tests tests/test_phrase_engine.cpp)
target_link_libraries(phrase_engine_tests PRIVATE yunpin_phrase_engine Threads::Threads)
add_test(NAME phrase_engine_tests COMMAND phrase_engine_tests)

add_executable(correction_learning_tests tests/test_correction_learning.cpp)
target_link_libraries(correction_learning_tests PRIVATE yunpin_phrase_engine Threads::Threads)
add_test(NAME correction_learning_tests COMMAND correction_learning_tests)

add_executable(yunpin_habit_report src/habit_report_cli.cpp)
target_link_libraries(yunpin_habit_report PRIVATE yunpin_phrase_engine Threads::Threads)

add_executable(phrase_engine_benchmark tests/benchmark_phrase_engine.cpp)
target_link_libraries(phrase_engine_benchmark PRIVATE yunpin_phrase_engine)
add_test(NAME phrase_engine_benchmark COMMAND phrase_engine_benchmark)
