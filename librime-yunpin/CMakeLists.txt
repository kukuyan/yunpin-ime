# SPDX-License-Identifier: Apache-2.0
cmake_minimum_required(VERSION 3.20)
project(rime_yunpin LANGUAGES CXX)

set(YUNPIN_ENGINE_ROOT "" CACHE PATH "Path to the YunPin phrase engine")
if(NOT YUNPIN_ENGINE_ROOT)
  if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/engine/src/phrase_engine.cpp")
    set(YUNPIN_ENGINE_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/engine")
  else()
    set(YUNPIN_ENGINE_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/../engine")
  endif()
endif()

if(NOT EXISTS "${YUNPIN_ENGINE_ROOT}/src/phrase_engine.cpp")
  message(FATAL_ERROR "YunPin phrase engine not found at ${YUNPIN_ENGINE_ROOT}")
endif()

set(yunpin_core_src
  "${YUNPIN_ENGINE_ROOT}/src/phrase_engine.cpp"
  src/snapshot_store.cpp
)

# When this directory is staged below librime/plugins, librime defines
# rime_library and consumes the four PARENT_SCOPE variables below. The plugin
# is intentionally merged into each frontend's rime library: Windows does not
# have a production external plugin loader, while a merged macOS build avoids
# an extra unsigned dylib and rpath boundary.
if(DEFINED rime_library)
  add_library(rime-yunpin-objs OBJECT
    ${yunpin_core_src}
    src/rime_yunpin_filter.cpp
    src/yunpin_module.cpp
  )
  target_include_directories(rime-yunpin-objs PRIVATE
    include
    "${YUNPIN_ENGINE_ROOT}/include"
    "${RIME_SOURCE_DIR}/src"
  )
  target_compile_features(rime-yunpin-objs PRIVATE cxx_std_17)
  if(BUILD_SHARED_LIBS)
    set_target_properties(rime-yunpin-objs PROPERTIES
      POSITION_INDEPENDENT_CODE ON
    )
  endif()
  set(plugin_name rime-yunpin PARENT_SCOPE)
  set(plugin_objs $<TARGET_OBJECTS:rime-yunpin-objs> PARENT_SCOPE)
  set(plugin_deps ${rime_library} PARENT_SCOPE)
  set(plugin_modules "yunpin" PARENT_SCOPE)
else()
  find_package(Threads REQUIRED)
  add_library(yunpin_snapshot STATIC ${yunpin_core_src})
  target_include_directories(yunpin_snapshot PUBLIC
    include
    "${YUNPIN_ENGINE_ROOT}/include"
  )
  target_compile_features(yunpin_snapshot PUBLIC cxx_std_17)
  if(MSVC)
    target_compile_options(yunpin_snapshot PRIVATE /W4 /WX /permissive-)
  else()
    target_compile_options(yunpin_snapshot PRIVATE
      -Wall -Wextra -Wpedantic -Werror
    )
  endif()

  enable_testing()
  add_executable(yunpin_snapshot_tests tests/snapshot_store_test.cpp)
  target_link_libraries(yunpin_snapshot_tests PRIVATE
    yunpin_snapshot
    Threads::Threads
  )
  add_test(NAME yunpin_snapshot_tests COMMAND yunpin_snapshot_tests)
endif()
