// SPDX-License-Identifier: GPL-3.0-only
#include <rime_api.h>
#include <windows.h>

#include <filesystem>
#include <iostream>
#include <iterator>
#include <system_error>

int main(int argc, char** argv) {
  if (argc != 4) {
    std::cerr
        << "usage: yunpin-rime-module-probe SHARED_DIR USER_DIR RIME_DLL\n";
    return 64;
  }
  std::filesystem::create_directories(argv[1]);
  std::filesystem::create_directories(argv[2]);

  RimeApi* api = rime_get_api();
  RIME_STRUCT(RimeTraits, traits);
  traits.shared_data_dir = argv[1];
  traits.user_data_dir = argv[2];
  traits.distribution_name = "YunPin Windows module probe";
  traits.distribution_code_name = "yunpin_windows_module_probe";
  traits.distribution_version = "1";
  traits.app_name = "rime.yunpin_windows_module_probe";
  traits.min_log_level = 2;
  traits.log_dir = "";

  api->setup(&traits);
  api->initialize(&traits);
  const bool octagram_registered = api->find_module("octagram") != nullptr;
  const bool grammar_registered = api->find_module("grammar") != nullptr;
  const bool yunpin_registered = api->find_module("yunpin") != nullptr;

  wchar_t loaded_path[32768] = {};
  const HMODULE rime_module = GetModuleHandleW(L"rime.dll");
  const DWORD loaded_path_size =
      rime_module
          ? GetModuleFileNameW(rime_module, loaded_path,
                               static_cast<DWORD>(std::size(loaded_path)))
          : 0;
  std::error_code path_error;
  const bool runtime_identity_exact =
      loaded_path_size > 0 && loaded_path_size < std::size(loaded_path) &&
      std::filesystem::equivalent(std::filesystem::path(loaded_path),
                                  std::filesystem::path(argv[3]), path_error) &&
      !path_error;
  std::cout << "octagram_module_registered="
            << (octagram_registered ? "true" : "false") << '\n'
            << "grammar_module_registered="
            << (grammar_registered ? "true" : "false") << '\n'
            << "yunpin_module_registered="
            << (yunpin_registered ? "true" : "false") << '\n'
            << "rime_runtime_identity_exact="
            << (runtime_identity_exact ? "true" : "false") << '\n';
  api->finalize();
  return octagram_registered && grammar_registered && yunpin_registered &&
                 runtime_identity_exact
             ? 0
             : 1;
}
