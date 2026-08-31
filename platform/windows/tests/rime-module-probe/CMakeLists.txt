# SPDX-License-Identifier: GPL-3.0-only
cmake_minimum_required(VERSION 3.20)
project(yunpin_rime_module_probe LANGUAGES CXX)

if(NOT WIN32)
  message(FATAL_ERROR "The YunPin Rime module probe is Windows-only")
endif()
if(NOT IS_DIRECTORY "${RIME_INCLUDE_DIR}")
  message(FATAL_ERROR "RIME_INCLUDE_DIR is missing")
endif()
if(NOT EXISTS "${RIME_IMPORT_LIBRARY}")
  message(FATAL_ERROR "RIME_IMPORT_LIBRARY is missing")
endif()

add_executable(yunpin-rime-module-probe main.cpp)
target_compile_features(yunpin-rime-module-probe PRIVATE cxx_std_17)
target_compile_options(yunpin-rime-module-probe PRIVATE /W4 /WX /permissive-)
target_include_directories(yunpin-rime-module-probe PRIVATE "${RIME_INCLUDE_DIR}")
target_link_libraries(yunpin-rime-module-probe PRIVATE "${RIME_IMPORT_LIBRARY}")
