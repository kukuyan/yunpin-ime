# SPDX-License-Identifier: Apache-2.0
cmake_minimum_required(VERSION 3.22.1)
project(yunpin_mobile_jni LANGUAGES CXX)

set(MOBILE_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/../../../../..")
set(YUNPIN_ROOT "${MOBILE_ROOT}/..")

add_library(yunpin_mobile_jni SHARED
  yunpin_mobile_jni.cpp
  "${MOBILE_ROOT}/shared/src/yunpin_mobile_core.cpp"
  "${YUNPIN_ROOT}/engine/src/phrase_engine.cpp"
  "${YUNPIN_ROOT}/librime-yunpin/src/snapshot_store.cpp"
)
target_include_directories(yunpin_mobile_jni PRIVATE
  "${MOBILE_ROOT}/shared/include"
  "${YUNPIN_ROOT}/engine/include"
  "${YUNPIN_ROOT}/librime-yunpin/include"
)
target_compile_features(yunpin_mobile_jni PRIVATE cxx_std_17)
