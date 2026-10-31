// SPDX-License-Identifier: Apache-2.0
// Exercises production Windows spool security without removing its synthetic
// fixture. The path is printed so an operator can manage it separately.
#undef NDEBUG
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <aclapi.h>

#include <array>
#include <atomic>
#include <cassert>
#include <chrono>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <sstream>
#include <thread>

static std::atomic<unsigned> security_sets{0};
static std::atomic<unsigned> blocked_deletes{0};
static DWORD WINAPI CountedSetSecurityInfo(HANDLE handle, SE_OBJECT_TYPE type,
    SECURITY_INFORMATION info, PSID owner, PSID group, PACL dacl, PACL sacl) {
  ++security_sets;
  return ::SetSecurityInfo(handle, type, info, owner, group, dacl, sacl);
}
static BOOL WINAPI BlockDeleteFileW(LPCWSTR) {
  ++blocked_deletes;
  SetLastError(ERROR_ACCESS_DENIED);
  return FALSE;
}
#define SetSecurityInfo CountedSetSecurityInfo
#define DeleteFileW BlockDeleteFileW
#include "../src/native_selection_events.cpp"
#undef DeleteFileW
#undef SetSecurityInfo

namespace fs = std::filesystem;

// Some elevated Windows environments choose Administrators as the default
// owner even when creating a directory with a security descriptor. Set the
// owner only on the synthetic directories created by this test.
static bool SetSyntheticOwner(const fs::path& path) {
  std::vector<unsigned char> user_storage;
  PSID user = nullptr;
  if (!yunpin::GetCurrentUserSid(&user_storage, &user)) return false;
  HANDLE handle = CreateFileW(path.c_str(), WRITE_OWNER | READ_CONTROL,
      FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, nullptr,
      OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT,
      nullptr);
  if (handle == INVALID_HANDLE_VALUE) return false;
  const DWORD result = ::SetSecurityInfo(handle, SE_FILE_OBJECT,
      OWNER_SECURITY_INFORMATION, user, nullptr, nullptr, nullptr);
  CloseHandle(handle);
  return result == ERROR_SUCCESS;
}

static bool AddSyntheticExtraAce(const fs::path& path) {
  HANDLE handle = CreateFileW(path.c_str(), READ_CONTROL | WRITE_DAC,
      FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, nullptr,
      OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT,
      nullptr);
  if (handle == INVALID_HANDLE_VALUE) return false;
  PACL old_acl = nullptr;
  PSECURITY_DESCRIPTOR descriptor = nullptr;
  DWORD result = GetSecurityInfo(handle, SE_FILE_OBJECT,
      DACL_SECURITY_INFORMATION, nullptr, nullptr, &old_acl, nullptr, &descriptor);
  std::array<unsigned char, SECURITY_MAX_SID_SIZE> users_storage{};
  DWORD users_size = static_cast<DWORD>(users_storage.size());
  PSID users = users_storage.data();
  PACL added_acl = nullptr;
  if (result == ERROR_SUCCESS &&
      CreateWellKnownSid(WinBuiltinUsersSid, nullptr, users, &users_size)) {
    EXPLICIT_ACCESSW entry{};
    entry.grfAccessPermissions = FILE_GENERIC_READ;
    entry.grfAccessMode = GRANT_ACCESS;
    entry.grfInheritance = OBJECT_INHERIT_ACE | CONTAINER_INHERIT_ACE;
    BuildTrusteeWithSidW(&entry.Trustee, users);
    result = SetEntriesInAclW(1, &entry, old_acl, &added_acl);
    if (result == ERROR_SUCCESS) {
      result = ::SetSecurityInfo(handle, SE_FILE_OBJECT,
          DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
          nullptr, nullptr, added_acl, nullptr);
    }
  } else {
    result = ERROR_INVALID_DATA;
  }
  if (added_acl) LocalFree(added_acl);
  if (descriptor) LocalFree(descriptor);
  CloseHandle(handle);
  return result == ERROR_SUCCESS;
}

int main() {
  // Windows CI may expose TEMP through an 8.3 alias such as RUNNER~1.
  // Production path checks compare the final long path of each handle, so
  // use the canonical directory for this synthetic fixture as well.
  const fs::path base = fs::canonical(fs::temp_directory_path()) /
      (L"yunpin-acl-revalidation-" + std::to_wstring(GetCurrentProcessId()) +
       L"-" + std::to_wstring(GetTickCount64()));
  assert(!fs::exists(base));
  assert(fs::create_directories(base));
  yunpin::PrivateWindowsSecurity security;
  assert(security.ready());
  fs::path current = base;
  for (const wchar_t* part : {L"YunPinIME", L"sync", L"native-events", L"incoming"}) {
    current /= part;
    assert(CreateDirectoryW(current.c_str(), security.attributes(true)));
    assert(SetSyntheticOwner(current));
  }
  const fs::path incoming = current;

  security_sets.store(0);
  const bool initial_ready = yunpin::EnsurePrivateDirectory(incoming);
  if (!initial_ready) {
    std::vector<yunpin::WindowsPathComponent> chain;
    std::wcerr << L"synthetic path: " << incoming.native()
               << L"; path_chain="
               << yunpin::InspectWindowsPathChain(incoming, true, &chain)
               << L"; incoming_security="
               << yunpin::ValidatePrivateWindowsPath(incoming, true) << L'\n';
  }
  assert(initial_ready);
  assert(security_sets.load() == 0);  // No redundant recursive ACL update.
  assert(yunpin::ValidatePrivateWindowsPath(incoming, true));

  assert(AddSyntheticExtraAce(incoming));
  assert(!yunpin::ValidatePrivateWindowsPath(incoming, true));
  security_sets.store(0);
  assert(yunpin::EnsurePrivateDirectory(incoming));
  assert(security_sets.load() == 1);  // Unsafe descriptor must still be fixed.
  assert(yunpin::ValidatePrivateWindowsPath(incoming, true));
  security_sets.store(0);
  assert(yunpin::EnsurePrivateDirectory(incoming));
  assert(security_sets.load() == 0);

  const auto dir = incoming.u8string();
  security_sets.store(0);
  assert(YunPinStartNativeSelectionSpoolerV1(dir.c_str()));
  assert(security_sets.load() == 0);
  assert(yunpin::NativeSelectionEventQueue::Instance().TryPublish(
      "合成测试词", "he cheng ce shi ci"));
  fs::path event;
  const auto until = std::chrono::steady_clock::now() + std::chrono::seconds(10);
  while (std::chrono::steady_clock::now() < until && event.empty()) {
    for (const auto& entry : fs::directory_iterator(incoming)) {
      if (entry.path().extension() == L".json") event = entry.path();
    }
    if (event.empty()) std::this_thread::sleep_for(std::chrono::milliseconds(50));
  }
  YunPinStopNativeSelectionSpoolerV1();
  assert(!event.empty());
  std::ifstream input(event, std::ios::binary);
  std::ostringstream contents;
  contents << input.rdbuf();
  assert(contents.str().find("合成测试词") != std::string::npos);
  assert(blocked_deletes.load() == 0);
  std::wcout << L"Synthetic fixture retained: " << base.native() << L'\n';
  return 0;
}
