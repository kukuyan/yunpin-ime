# SPDX-License-Identifier: Apache-2.0
cmake_minimum_required(VERSION 3.20)
project(yunpin_mobile_core LANGUAGES CXX)

set(YUNPIN_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/..")

add_library(yunpin_mobile_core STATIC
  shared/src/yunpin_mobile_core.cpp
  "${YUNPIN_ROOT}/engine/src/phrase_engine.cpp"
  "${YUNPIN_ROOT}/librime-yunpin/src/snapshot_store.cpp"
)
target_include_directories(yunpin_mobile_core PUBLIC
  shared/include
  "${YUNPIN_ROOT}/engine/include"
  "${YUNPIN_ROOT}/librime-yunpin/include"
)
target_compile_features(yunpin_mobile_core PUBLIC cxx_std_17)
if(MSVC)
  target_compile_options(yunpin_mobile_core PRIVATE /W4 /WX /permissive- /utf-8)
else()
  target_compile_options(yunpin_mobile_core PRIVATE -Wall -Wextra -Wpedantic -Werror)
endif()

enable_testing()
add_executable(yunpin_mobile_core_tests shared/tests/mobile_core_test.cpp)
target_link_libraries(yunpin_mobile_core_tests PRIVATE yunpin_mobile_core)
add_test(NAME yunpin_mobile_core_tests COMMAND yunpin_mobile_core_tests)
