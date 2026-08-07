# SPDX-License-Identifier: GPL-3.0-only
.PHONY: test prepare build-librime build package

ROOT := $(abspath ../..)
BUILD_ROOT ?= $(ROOT)/build/macos

test:
	PYTHONDONTWRITEBYTECODE=1 python3 tests/test_macos_integration.py -v

prepare:
	YUNPIN_MACOS_BUILD_ROOT="$(BUILD_ROOT)" scripts/prepare-source.sh "$(BUILD_ROOT)/squirrel"

build-librime:
	YUNPIN_MACOS_BUILD_ROOT="$(BUILD_ROOT)" scripts/build-librime-yunpin.sh "$(BUILD_ROOT)/squirrel"

build:
	YUNPIN_MACOS_BUILD_ROOT="$(BUILD_ROOT)" scripts/build-preview.sh

package: build
	YUNPIN_MACOS_BUILD_ROOT="$(BUILD_ROOT)" scripts/package-preview.sh
