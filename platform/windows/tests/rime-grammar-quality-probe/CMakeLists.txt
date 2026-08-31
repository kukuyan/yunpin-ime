# SPDX-License-Identifier: GPL-3.0-only
cmake_minimum_required(VERSION 3.20)
project(yunpin_rime_grammar_quality_probe LANGUAGES CXX)

if(NOT WIN32)
  message(FATAL_ERROR "The YunPin Rime grammar quality probe is Windows-only")
endif()
if(NOT IS_DIRECTORY "${RIME_INCLUDE_DIR}")
  message(FATAL_ERROR "RIME_INCLUDE_DIR is missing")
endif()
if(NOT EXISTS "${RIME_IMPORT_LIBRARY}")
  message(FATAL_ERROR "RIME_IMPORT_LIBRARY is missing")
endif()

add_executable(yunpin-rime-grammar-quality-probe main.cpp)
target_compile_features(yunpin-rime-grammar-quality-probe PRIVATE cxx_std_17)
target_compile_options(
  yunpin-rime-grammar-quality-probe PRIVATE /W4 /WX /permissive- /utf-8
)
target_include_directories(
  yunpin-rime-grammar-quality-probe PRIVATE "${RIME_INCLUDE_DIR}"
)
target_link_libraries(
  yunpin-rime-grammar-quality-probe PRIVATE "${RIME_IMPORT_LIBRARY}" Psapi.lib
)
