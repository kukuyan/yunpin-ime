# SPDX-License-Identifier: Apache-2.0
cmake_minimum_required(VERSION 3.20)
project(rime_yunpin LANGUAGES CXX)

if(MSVC)
  # The adapter, tests and snapshot fixtures contain Chinese UTF-8 text.
  # This also applies when the plugin is merged into librime on Windows.
  add_compile_options(/utf-8)
endif()

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
  "${YUNPIN_ENGINE_ROOT}/src/correction_learning.cpp"
  src/native_selection_events.cpp
  src/replay_native.cpp
  src/replay_spooler.cpp
  src/snapshot_store.cpp
  src/session_learning.cpp
  src/typo_correction.cpp
)

# When this directory is staged below librime/plugins, librime defines
# rime_library and consumes the four PARENT_SCOPE variables below. The plugin
# is intentionally merged into each frontend's rime library: Windows does not
# have a production external plugin loader, while a merged macOS build avoids
# an extra unsigned dylib and rpath boundary.
if(DEFINED rime_library)
  add_library(rime-yunpin-objs OBJECT
    ${yunpin_core_src}
    src/rime_yunpin_comment_filter.cpp
    src/rime_yunpin_corrector.cpp
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
  if(WIN32)
    set(plugin_deps ${rime_library} Advapi32 Shell32 Ole32 Uuid PARENT_SCOPE)
  else()
    set(plugin_deps ${rime_library} PARENT_SCOPE)
  endif()
  set(plugin_modules "yunpin" PARENT_SCOPE)

else()
  find_package(Threads REQUIRED)
  add_library(yunpin_snapshot STATIC ${yunpin_core_src})
  target_include_directories(yunpin_snapshot PUBLIC
    include
    "${YUNPIN_ENGINE_ROOT}/include"
  )
  target_compile_features(yunpin_snapshot PUBLIC cxx_std_17)
  if(WIN32)
    target_link_libraries(yunpin_snapshot PUBLIC Advapi32 Shell32 Ole32 Uuid)
  endif()
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

  add_executable(yunpin_snapshot_benchmark
    tests/benchmark_snapshot_store.cpp
  )
  target_link_libraries(yunpin_snapshot_benchmark PRIVATE
    yunpin_snapshot
    Threads::Threads
  )
  if(WIN32)
    target_link_libraries(yunpin_snapshot_benchmark PRIVATE Psapi)
  endif()
  add_test(NAME yunpin_snapshot_benchmark COMMAND yunpin_snapshot_benchmark)

  add_executable(yunpin_session_learning_tests
    tests/session_learning_test.cpp
  )
  target_link_libraries(yunpin_session_learning_tests PRIVATE
    yunpin_snapshot
    Threads::Threads
  )
  add_test(NAME yunpin_session_learning_tests
    COMMAND yunpin_session_learning_tests)

  add_executable(yunpin_native_selection_events_tests
    tests/native_selection_events_test.cpp
  )
  target_link_libraries(yunpin_native_selection_events_tests PRIVATE
    yunpin_snapshot
    Threads::Threads
  )
  add_test(NAME yunpin_native_selection_events_tests
    COMMAND yunpin_native_selection_events_tests)

  add_executable(yunpin_replay_native_tests
    tests/replay_native_test.cpp
  )
  target_link_libraries(yunpin_replay_native_tests PRIVATE
    yunpin_snapshot
    Threads::Threads
  )
  add_test(NAME yunpin_replay_native_tests
    COMMAND yunpin_replay_native_tests)

  add_executable(yunpin_replay_spooler_tests
    tests/replay_spooler_test.cpp
  )
  target_link_libraries(yunpin_replay_spooler_tests PRIVATE
    yunpin_snapshot
    Threads::Threads
  )
  add_test(NAME yunpin_replay_spooler_tests
    COMMAND yunpin_replay_spooler_tests)

  find_package(Python3 COMPONENTS Interpreter QUIET)
  find_program(YUNPIN_GO_EXECUTABLE NAMES go)
  if(Python3_Interpreter_FOUND AND YUNPIN_GO_EXECUTABLE)
    add_test(NAME yunpin_replay_host_e2e
      COMMAND "${Python3_EXECUTABLE}"
        "${CMAKE_CURRENT_SOURCE_DIR}/tests/replay_host_e2e.py"
        --fixture "$<TARGET_FILE:yunpin_replay_spooler_tests>"
        --go "${YUNPIN_GO_EXECUTABLE}"
        --module "${CMAKE_CURRENT_SOURCE_DIR}/../replaylab"
    )
  else()
    message(WARNING
      "Python 3 or Go is unavailable; synthetic Replay Lab host/report E2E is disabled")
  endif()

  add_executable(yunpin_typo_correction_tests
    tests/typo_correction_test.cpp
  )
  target_link_libraries(yunpin_typo_correction_tests PRIVATE
    yunpin_snapshot
    Threads::Threads
  )
  add_test(NAME yunpin_typo_correction_tests
    COMMAND yunpin_typo_correction_tests)

  # Candidate ordering is decided by the filter, which normally only compiles
  # inside a librime tree. tests/rime_stubs declares the small slice of the
  # librime API the filter uses so the ordering rules stay testable on a
  # machine without librime, Boost or glog. The filter, snapshot store and
  # phrase engine sources here are the production ones.
  add_executable(yunpin_filter_behaviour_tests
    tests/filter_behaviour_test.cpp
    src/rime_yunpin_filter.cpp
  )
  target_include_directories(yunpin_filter_behaviour_tests PRIVATE
    tests/rime_stubs
    src
  )
  target_link_libraries(yunpin_filter_behaviour_tests PRIVATE
    yunpin_snapshot
    Threads::Threads
  )
  if(MSVC)
    target_compile_options(yunpin_filter_behaviour_tests PRIVATE /W4 /WX /permissive-)
  else()
    target_compile_options(yunpin_filter_behaviour_tests PRIVATE
      -Wall -Wextra -Wpedantic -Werror
    )
  endif()
  add_test(NAME yunpin_filter_behaviour_tests COMMAND yunpin_filter_behaviour_tests)

  # Candidate annotation visibility is an independent, display-only filter.
  # Compile its production source against the same version-locked librime
  # stubs so ShadowCandidate identity and translation exhaustion stay covered
  # without requiring a frontend build.
  add_executable(yunpin_comment_filter_behaviour_tests
    tests/comment_filter_behaviour_test.cpp
    src/rime_yunpin_comment_filter.cpp
  )
  target_include_directories(yunpin_comment_filter_behaviour_tests PRIVATE
    tests/rime_stubs
    src
  )
  target_compile_features(yunpin_comment_filter_behaviour_tests PRIVATE
    cxx_std_17
  )
  if(MSVC)
    target_compile_options(yunpin_comment_filter_behaviour_tests PRIVATE
      /W4 /WX /permissive-
    )
  else()
    target_compile_options(yunpin_comment_filter_behaviour_tests PRIVATE
      -Wall -Wextra -Wpedantic -Werror
    )
  endif()
  add_test(NAME yunpin_comment_filter_behaviour_tests
    COMMAND yunpin_comment_filter_behaviour_tests)
endif()
