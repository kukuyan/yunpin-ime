// SPDX-License-Identifier: Apache-2.0

#undef NDEBUG

#include "yunpin/native_selection_events.hpp"

#include <array>
#include <atomic>
#include <cassert>
#include <chrono>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <set>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

#if defined(_WIN32)
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <aclapi.h>
#include <windows.h>
#else
#include <sys/wait.h>
#include <sys/stat.h>
#include <unistd.h>
#endif

namespace {

using yunpin::NativeSelectionEvent;
using yunpin::NativeSelectionEventQueue;
namespace fs = std::filesystem;

void Drain() {
  NativeSelectionEvent event;
  while (NativeSelectionEventQueue::Instance().TryPop(&event)) {
  }
}

int RunCrossProcessSpoolChild(const std::string& incoming,
                              std::string_view phrase,
                              std::string_view pinyin) {
  if (!YunPinStartNativeSelectionSpoolerV1(incoming.c_str())) {
    return 2;
  }
  if (!NativeSelectionEventQueue::Instance().TryPublish(phrase, pinyin)) {
    YunPinStopNativeSelectionSpoolerV1();
    return 3;
  }
  YunPinStopNativeSelectionSpoolerV1();
  return 0;
}

void PrepareEmptyPrivateSpool(const fs::path& incoming) {
  assert(!fs::exists(incoming));
  const std::string incoming_utf8 = incoming.u8string();
  assert(YunPinStartNativeSelectionSpoolerV1(incoming_utf8.c_str()));
  YunPinStopNativeSelectionSpoolerV1();
  assert(fs::is_directory(incoming));
}

#if defined(_WIN32)

constexpr ACCESS_MASK kTestPrivateFullControl = 0x001f01ff;

class WindowsTestPrivateSecurity {
 public:
  WindowsTestPrivateSecurity() {
    HANDLE token = nullptr;
    if (!OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &token)) {
      return;
    }
    DWORD required = 0;
    (void)GetTokenInformation(token, TokenUser, nullptr, 0, &required);
    if (GetLastError() != ERROR_INSUFFICIENT_BUFFER || required == 0) {
      (void)CloseHandle(token);
      return;
    }
    user_storage_.resize(required);
    const bool have_user =
        GetTokenInformation(token, TokenUser, user_storage_.data(), required,
                            &required) != 0;
    (void)CloseHandle(token);
    if (!have_user) {
      return;
    }
    user_sid_ = reinterpret_cast<TOKEN_USER*>(user_storage_.data())->User.Sid;
    DWORD system_size = static_cast<DWORD>(system_storage_.size());
    system_sid_ = system_storage_.data();
    ready_ = IsValidSid(user_sid_) != 0 &&
             CreateWellKnownSid(WinLocalSystemSid, nullptr, system_sid_,
                                &system_size) != 0 &&
             BuildAcl(false, &file_acl_) && BuildAcl(true, &directory_acl_);
  }

  ~WindowsTestPrivateSecurity() {
    if (file_acl_) {
      (void)LocalFree(file_acl_);
    }
    if (directory_acl_) {
      (void)LocalFree(directory_acl_);
    }
  }

  bool Harden(const fs::path& path, bool directory) const {
    const DWORD attributes = GetFileAttributesW(path.c_str());
    if (!ready_ || attributes == INVALID_FILE_ATTRIBUTES ||
        (attributes & FILE_ATTRIBUTE_REPARSE_POINT) != 0 ||
        (((attributes & FILE_ATTRIBUTE_DIRECTORY) != 0) != directory)) {
      return false;
    }
    PACL acl = directory ? directory_acl_ : file_acl_;
    const DWORD result = SetNamedSecurityInfoW(
        const_cast<wchar_t*>(path.c_str()), SE_FILE_OBJECT,
        OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION |
            PROTECTED_DACL_SECURITY_INFORMATION,
        user_sid_, nullptr, acl, nullptr);
    return result == ERROR_SUCCESS && Validate(path, directory);
  }

  bool Validate(const fs::path& path, bool directory) const {
    const DWORD attributes = GetFileAttributesW(path.c_str());
    if (!ready_ || attributes == INVALID_FILE_ATTRIBUTES ||
        (attributes & FILE_ATTRIBUTE_REPARSE_POINT) != 0 ||
        (((attributes & FILE_ATTRIBUTE_DIRECTORY) != 0) != directory)) {
      return false;
    }
    PSID owner = nullptr;
    PACL dacl = nullptr;
    PSECURITY_DESCRIPTOR descriptor = nullptr;
    const DWORD result = GetNamedSecurityInfoW(
        const_cast<wchar_t*>(path.c_str()), SE_FILE_OBJECT,
        OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION, &owner,
        nullptr, &dacl, nullptr, &descriptor);
    BOOL owner_defaulted = TRUE;
    BOOL dacl_present = FALSE;
    BOOL dacl_defaulted = TRUE;
    PSID descriptor_owner = nullptr;
    PACL descriptor_dacl = nullptr;
    SECURITY_DESCRIPTOR_CONTROL control = 0;
    DWORD revision = 0;
    ACL_SIZE_INFORMATION information{};
    bool valid = result == ERROR_SUCCESS && descriptor && owner && dacl &&
                 GetSecurityDescriptorOwner(descriptor, &descriptor_owner,
                                            &owner_defaulted) != 0 &&
                 descriptor_owner && !owner_defaulted &&
                 EqualSid(owner, user_sid_) != 0 &&
                 EqualSid(descriptor_owner, user_sid_) != 0 &&
                 GetSecurityDescriptorControl(descriptor, &control,
                                              &revision) != 0 &&
                 (control & SE_DACL_PRESENT) != 0 &&
                 (control & SE_DACL_PROTECTED) != 0 &&
                 (control & (SE_DACL_DEFAULTED |
                             SE_DACL_AUTO_INHERIT_REQ)) == 0 &&
                 GetSecurityDescriptorDacl(descriptor, &dacl_present,
                                           &descriptor_dacl,
                                           &dacl_defaulted) != 0 &&
                 dacl_present && !dacl_defaulted && descriptor_dacl == dacl &&
                 GetAclInformation(dacl, &information, sizeof(information),
                                   AclSizeInformation) != 0 &&
                 information.AceCount == 2;
    const BYTE expected_flags = directory
                                    ? OBJECT_INHERIT_ACE |
                                          CONTAINER_INHERIT_ACE
                                    : static_cast<BYTE>(0);
    bool user_seen = false;
    bool system_seen = false;
    for (DWORD index = 0; valid && index < information.AceCount; ++index) {
      void* raw = nullptr;
      valid = GetAce(dacl, index, &raw) != 0 && raw;
      if (!valid) {
        break;
      }
      const auto* ace = static_cast<const ACCESS_ALLOWED_ACE*>(raw);
      PSID sid = const_cast<DWORD*>(&ace->SidStart);
      valid = ace->Header.AceType == ACCESS_ALLOWED_ACE_TYPE &&
              ace->Header.AceFlags == expected_flags &&
              (ace->Header.AceFlags & INHERITED_ACE) == 0 &&
              ace->Mask == kTestPrivateFullControl && IsValidSid(sid) != 0;
      if (!valid) {
        break;
      }
      if (EqualSid(sid, user_sid_) != 0 && !user_seen) {
        user_seen = true;
      } else if (EqualSid(sid, system_sid_) != 0 && !system_seen) {
        system_seen = true;
      } else {
        valid = false;
      }
    }
    if (descriptor) {
      (void)LocalFree(descriptor);
    }
    return valid && user_seen && system_seen;
  }

 private:
  bool BuildAcl(bool directory, PACL* output) {
    std::array<EXPLICIT_ACCESSW, 2> access{};
    for (EXPLICIT_ACCESSW& entry : access) {
      entry.grfAccessPermissions = kTestPrivateFullControl;
      entry.grfAccessMode = SET_ACCESS;
      entry.grfInheritance = directory
                                 ? OBJECT_INHERIT_ACE |
                                       CONTAINER_INHERIT_ACE
                                 : NO_INHERITANCE;
    }
    BuildTrusteeWithSidW(&access[0].Trustee, user_sid_);
    BuildTrusteeWithSidW(&access[1].Trustee, system_sid_);
    return output && SetEntriesInAclW(2, access.data(), nullptr, output) ==
                         ERROR_SUCCESS;
  }

  std::vector<unsigned char> user_storage_;
  PSID user_sid_{nullptr};
  std::array<unsigned char, SECURITY_MAX_SID_SIZE> system_storage_{};
  PSID system_sid_{nullptr};
  PACL file_acl_{nullptr};
  PACL directory_acl_{nullptr};
  bool ready_{false};
};

WindowsTestPrivateSecurity& TestWindowsSecurity() {
  static WindowsTestPrivateSecurity security;
  return security;
}

void HardenWindowsTestFile(const fs::path& path) {
  assert(TestWindowsSecurity().Harden(path, false));
}

void AssertWindowsPrivatePath(const fs::path& path, bool directory) {
  assert(TestWindowsSecurity().Validate(path, directory));
}

#endif

void TestRoundTripAndUniqueIds() {
  Drain();
  auto& queue = NativeSelectionEventQueue::Instance();
  assert(queue.TryPublish("数据库", "shu ju ku"));
  assert(queue.TryPublish("版本", "ban ben"));

  std::set<std::string> identifiers;
  NativeSelectionEvent first;
  NativeSelectionEvent second;
  assert(queue.TryPop(&first));
  assert(queue.TryPop(&second));
  assert(first.phrase == "数据库");
  assert(first.pinyin == "shu ju ku");
  assert(second.phrase == "版本");
  identifiers.insert(first.event_id);
  identifiers.insert(second.event_id);
  assert(identifiers.size() == 2);
  for (const auto& identifier : identifiers) {
    assert(!identifier.empty());
    assert(identifier.size() <= NativeSelectionEventQueue::kMaxEventIdBytes);
    for (const unsigned char byte : identifier) {
      assert((byte >= 'a' && byte <= 'z') ||
             (byte >= 'A' && byte <= 'Z') ||
             (byte >= '0' && byte <= '9') || byte == '_' || byte == '-');
    }
  }
}

void TestInvalidAndOversizedValuesFailClosed() {
  Drain();
  auto& queue = NativeSelectionEventQueue::Instance();
  const std::uint64_t dropped_before = queue.dropped();
  assert(!queue.TryPublish("", "shu ju ku"));
  assert(!queue.TryPublish(std::string("broken\xff", 7), "broken"));
  assert(!queue.TryPublish("数据库", "SHU-JU-KU"));
  assert(!queue.TryPublish(
      std::string(NativeSelectionEventQueue::kMaxPhraseBytes + 1, 'x'),
      "x"));
  assert(queue.dropped() >= dropped_before + 4);
  assert(queue.size() == 0);
}

void TestBoundedQueueDropsWithoutOverwriting() {
  Drain();
  auto& queue = NativeSelectionEventQueue::Instance();
  for (std::size_t index = 0;
       index < NativeSelectionEventQueue::kCapacity; ++index) {
    assert(queue.TryPublish("测试", "ce shi"));
  }
  const std::uint64_t dropped_before = queue.dropped();
  assert(!queue.TryPublish("溢出", "yi chu"));
  assert(queue.dropped() == dropped_before + 1);

  std::size_t popped = 0;
  NativeSelectionEvent event;
  while (queue.TryPop(&event)) {
    assert(event.phrase == "测试");
    ++popped;
  }
  assert(popped == NativeSelectionEventQueue::kCapacity);
}

void TestStableCBoundary() {
  Drain();
  assert(NativeSelectionEventQueue::Instance().TryPublish(
      "输入习惯", "shu ru xi guan"));
  YunPinNativeSelectionEventV1 event{};
  assert(YunPinTryPopNativeSelectionEventV1(&event));
  assert(event.version == 1);
  assert(std::strcmp(event.phrase, "输入习惯") == 0);
  assert(std::strcmp(event.pinyin, "shu ru xi guan") == 0);
  assert(std::strlen(event.event_id) > 0);
  assert(!YunPinTryPopNativeSelectionEventV1(&event));
  assert(!YunPinTryPopNativeSelectionEventV1(nullptr));
}

#if defined(_WIN32)
void AssertExplicitWindowsSpoolPathRejected(const fs::path& path) {
  const std::string utf8 = path.u8string();
  const bool started = YunPinStartNativeSelectionSpoolerV1(utf8.c_str());
  if (started) {
    YunPinStopNativeSelectionSpoolerV1();
  }
  assert(!started);
}

void TestWindowsUnsafePathsFailBeforeSpooling(const fs::path& root,
                                              const fs::path& incoming) {
  AssertExplicitWindowsSpoolPathRejected(
      root / "YunPinIME" / "sync" / "native-events" / "staging" / ".." /
      "incoming");
  AssertExplicitWindowsSpoolPathRejected(
      root / "bad." / "YunPinIME" / "sync" / "native-events" / "incoming");
  AssertExplicitWindowsSpoolPathRejected(
      root / "NUL" / "YunPinIME" / "sync" / "native-events" / "incoming");
  AssertExplicitWindowsSpoolPathRejected(
      root / "stream:ads" / "YunPinIME" / "sync" / "native-events" /
      "incoming");
  AssertExplicitWindowsSpoolPathRejected(
      fs::path(std::wstring(L"\\\\?\\") + incoming.wstring()));

  const fs::path target = root / "junction-target";
  const fs::path junction = root / "junction";
  fs::create_directories(target);
  if (CreateSymbolicLinkW(junction.c_str(), target.c_str(),
                          SYMBOLIC_LINK_FLAG_DIRECTORY | 0x2) != 0) {
    AssertExplicitWindowsSpoolPathRejected(
        junction / "YunPinIME" / "sync" / "native-events" / "incoming");
  }
}
#endif

void TestAtomicPrivateSpoolAndLifecycle() {
  Drain();
  const auto unique = std::chrono::steady_clock::now()
                          .time_since_epoch()
                          .count();
  const fs::path root = fs::canonical(fs::temp_directory_path()) /
                        ("yunpin-native-spool-test-" +
                         std::to_string(unique));
#if defined(_WIN32)
  const fs::path incoming =
      root / "YunPinIME" / "sync" / "native-events" / "incoming";
#else
  const fs::path incoming =
      root / "YunPin" / "Sync" / "native-events" / "incoming";
#endif
  const std::string incoming_utf8 = incoming.u8string();

  assert(!YunPinStartNativeSelectionSpoolerV1(nullptr));
  assert(!YunPinStartNativeSelectionSpoolerV1("relative/path"));
#if defined(_WIN32)
  TestWindowsUnsafePathsFailBeforeSpooling(root, incoming);
#endif
  for (int cycle = 0; cycle < 10; ++cycle) {
    assert(YunPinStartNativeSelectionSpoolerV1(incoming_utf8.c_str()));
    assert(YunPinStartNativeSelectionSpoolerV1(incoming_utf8.c_str()));
    assert(!YunPinStartNativeSelectionSpoolerV1(
        (incoming_utf8 + "-different").c_str()));
    YunPinStopNativeSelectionSpoolerV1();
  }

  assert(YunPinStartNativeSelectionSpoolerV1(incoming_utf8.c_str()));
  assert(NativeSelectionEventQueue::Instance().TryPublish(
      "引号\"和反斜线\\", "yin hao he fan xie xian"));

  fs::path event_path;
  const auto deadline = std::chrono::steady_clock::now() +
                        std::chrono::seconds(3);
  while (std::chrono::steady_clock::now() < deadline) {
    if (fs::is_directory(incoming)) {
      for (const auto& entry : fs::directory_iterator(incoming)) {
        if (entry.path().extension() == ".json") {
          event_path = entry.path();
          break;
        }
      }
    }
    if (!event_path.empty()) {
      break;
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(10));
  }
  YunPinStopNativeSelectionSpoolerV1();

  assert(!event_path.empty());
  assert(fs::is_regular_file(fs::symlink_status(event_path)));
  std::ifstream input(event_path, std::ios::binary);
  std::ostringstream contents;
  contents << input.rdbuf();
  const std::string json = contents.str();
  assert(json.size() <= 4096);
  const std::string prefix = "{\"version\":1,\"event_id\":\"";
  assert(json.rfind(prefix, 0) == 0);
  const std::size_t identifier_end = json.find('"', prefix.size());
  assert(identifier_end != std::string::npos);
  const std::string identifier =
      json.substr(prefix.size(), identifier_end - prefix.size());
  const std::string expected =
      prefix + identifier +
      "\",\"phrase\":\"引号\\\"和反斜线\\\\\",\"pinyin\":\"yin hao he fan xie xian\"}\n";
  assert(json == expected);
  input.close();
#if defined(_WIN32)
  for (fs::path managed = incoming, stop = incoming.parent_path().parent_path()
                                            .parent_path().parent_path();
       managed != stop; managed = managed.parent_path()) {
    AssertWindowsPrivatePath(managed, true);
  }
  AssertWindowsPrivatePath(incoming / ".spool.lock", false);
  AssertWindowsPrivatePath(event_path, false);
#else
  struct stat status {};
  assert(::stat(event_path.c_str(), &status) == 0);
  assert((status.st_mode & 0777) == 0600);
  assert(::stat(incoming.c_str(), &status) == 0);
  assert((status.st_mode & 0777) == 0700);
#endif
  fs::remove_all(root);
}

void TestSpoolQuotaDropsWithoutBlockingOrGrowing() {
  Drain();
  const auto unique = std::chrono::steady_clock::now()
                          .time_since_epoch()
                          .count();
  const fs::path root = fs::canonical(fs::temp_directory_path()) /
                        ("yunpin-native-spool-quota-" +
                         std::to_string(unique));
#if defined(_WIN32)
  const fs::path incoming =
      root / "YunPinIME" / "sync" / "native-events" / "incoming";
#else
  const fs::path incoming =
      root / "YunPin" / "Sync" / "native-events" / "incoming";
#endif
  PrepareEmptyPrivateSpool(incoming);
  for (std::size_t index = 0; index < 2048; ++index) {
    const fs::path filler = incoming / ("filler-" + std::to_string(index));
    std::ofstream output(filler, std::ios::binary);
    output.put('x');
    output.close();
#if defined(_WIN32)
    HardenWindowsTestFile(filler);
#else
    fs::permissions(filler, fs::perms::owner_read | fs::perms::owner_write,
                    fs::perm_options::replace);
#endif
  }

  const std::uint64_t dropped_before =
      YunPinNativeSelectionSpoolDropCount();
  const std::string incoming_utf8 = incoming.u8string();
  assert(YunPinStartNativeSelectionSpoolerV1(incoming_utf8.c_str()));
  assert(NativeSelectionEventQueue::Instance().TryPublish("配额", "pei e"));
  const auto deadline = std::chrono::steady_clock::now() +
                        std::chrono::seconds(3);
  while (std::chrono::steady_clock::now() < deadline &&
         YunPinNativeSelectionSpoolDropCount() == dropped_before) {
    std::this_thread::sleep_for(std::chrono::milliseconds(10));
  }
  YunPinStopNativeSelectionSpoolerV1();
  assert(YunPinNativeSelectionSpoolDropCount() == dropped_before + 1);
  std::size_t payload_entries = 0;
  bool saw_lock = false;
  for (const auto& entry : fs::directory_iterator(incoming)) {
    if (entry.path().filename() == ".spool.lock") {
      saw_lock = true;
      assert(fs::file_size(entry.path()) == 0);
    } else {
      ++payload_entries;
    }
  }
  // The persistent cross-process lock is strictly validated but excluded
  // from payload quota, so an upgrade from an old full spool cannot deadlock.
  assert(saw_lock);
  assert(payload_entries == 2048);
  fs::remove_all(root);
}

void TestRecoversDurableTempBeforeWritingNextEvent() {
  Drain();
  const auto unique = std::chrono::steady_clock::now()
                          .time_since_epoch()
                          .count();
  const fs::path root = fs::canonical(fs::temp_directory_path()) /
                        ("yunpin-native-spool-recover-" +
                         std::to_string(unique));
#if defined(_WIN32)
  const fs::path incoming =
      root / "YunPinIME" / "sync" / "native-events" / "incoming";
#else
  const fs::path incoming =
      root / "YunPin" / "Sync" / "native-events" / "incoming";
#endif
  PrepareEmptyPrivateSpool(incoming);
  const fs::path staged = incoming / ".tmp-synthetic-crash-1a";
  const std::string staged_json =
      "{\"version\":1,\"event_id\":\"synthetic-crash\","
      "\"phrase\":\"恢复\",\"pinyin\":\"hui fu\"}\n";
  {
    std::ofstream output(staged, std::ios::binary);
    output << staged_json;
  }
#if defined(_WIN32)
  HardenWindowsTestFile(staged);
#else
  fs::permissions(staged, fs::perms::owner_read | fs::perms::owner_write,
                  fs::perm_options::replace);
#endif

  const std::string incoming_utf8 = incoming.u8string();
  assert(YunPinStartNativeSelectionSpoolerV1(incoming_utf8.c_str()));
  const fs::path recovered = incoming / "synthetic-crash.json";
  const auto deadline = std::chrono::steady_clock::now() +
                        std::chrono::seconds(3);
  while (std::chrono::steady_clock::now() < deadline &&
         !fs::exists(recovered)) {
    std::this_thread::sleep_for(std::chrono::milliseconds(10));
  }
  // Recovery must happen at startup even when the user types nothing else.
  YunPinStopNativeSelectionSpoolerV1();
  assert(!fs::exists(staged));
  std::ifstream input(recovered, std::ios::binary);
  std::ostringstream contents;
  contents << input.rdbuf();
  assert(contents.str() == staged_json);
  input.close();
  fs::remove_all(root);
}

void TestCrossProcessProducersRespectSharedQuota(
    const fs::path& executable) {
  Drain();
  const auto unique = std::chrono::steady_clock::now()
                          .time_since_epoch()
                          .count();
  const fs::path root = fs::canonical(fs::temp_directory_path()) /
                        ("yunpin-native-spool-processes-" +
                         std::to_string(unique));
#if defined(_WIN32)
  const fs::path incoming =
      root / "YunPinIME" / "sync" / "native-events" / "incoming";
#else
  const fs::path incoming =
      root / "YunPin" / "Sync" / "native-events" / "incoming";
#endif
  PrepareEmptyPrivateSpool(incoming);
  const std::string incoming_utf8 = incoming.u8string();
  // Leave exactly one payload slot.  Two independent frontend processes race
  // to fill it; the persistent flock must serialize scan+install so the
  // directory never exceeds the payload quota.
  for (std::size_t index = 0; index < 2047; ++index) {
    const fs::path filler = incoming / ("filler-" + std::to_string(index));
    std::ofstream output(filler, std::ios::binary);
    output.put('x');
    output.close();
#if defined(_WIN32)
    HardenWindowsTestFile(filler);
#else
    fs::permissions(filler,
                    fs::perms::owner_read | fs::perms::owner_write,
                    fs::perm_options::replace);
#endif
  }
#if !defined(_WIN32)
  std::array<pid_t, 2> children{};
  for (std::size_t index = 0; index < children.size(); ++index) {
    children[index] = ::fork();
    assert(children[index] >= 0);
    if (children[index] == 0) {
      const char* phrase = index == 0 ? "甲" : "乙";
      const char* pinyin = index == 0 ? "jia" : "yi";
      ::execl(executable.c_str(), executable.c_str(), "--spool-child",
              incoming_utf8.c_str(), phrase, pinyin,
              static_cast<char*>(nullptr));
      _exit(127);
    }
  }
  for (const pid_t child : children) {
    int status = 0;
    assert(::waitpid(child, &status, 0) == child);
    assert(WIFEXITED(status));
    assert(WEXITSTATUS(status) == 0);
  }
#else
  const std::string executable_utf8 = executable.u8string();
  assert(executable_utf8.find('"') == std::string::npos);
  assert(incoming_utf8.find('"') == std::string::npos);
  std::array<PROCESS_INFORMATION, 2> children{};
  for (std::size_t index = 0; index < children.size(); ++index) {
    const char* phrase = index == 0 ? "alpha" : "beta";
    const char* pinyin = index == 0 ? "alpha" : "beta";
    std::string command = "\"" + executable_utf8 +
                          "\" --spool-child \"" + incoming_utf8 +
                          "\" " + phrase + " " + pinyin;
    STARTUPINFOA startup{};
    startup.cb = sizeof(startup);
    assert(CreateProcessA(executable_utf8.c_str(), command.data(), nullptr,
                          nullptr, FALSE, CREATE_NO_WINDOW, nullptr, nullptr,
                          &startup, &children[index]) != 0);
    (void)CloseHandle(children[index].hThread);
  }
  for (PROCESS_INFORMATION& child : children) {
    assert(WaitForSingleObject(child.hProcess, 10000) == WAIT_OBJECT_0);
    DWORD exit_code = 0;
    assert(GetExitCodeProcess(child.hProcess, &exit_code) != 0);
    assert(exit_code == 0);
    (void)CloseHandle(child.hProcess);
  }
#endif
  std::size_t payload_entries = 0;
  std::size_t json_entries = 0;
  for (const auto& entry : fs::directory_iterator(incoming)) {
    if (entry.path().filename() == ".spool.lock") {
      continue;
    }
    ++payload_entries;
    if (entry.path().extension() == ".json") {
      ++json_entries;
    }
  }
  assert(payload_entries == 2048);
  assert(json_entries == 1);
  fs::remove_all(root);
}

void TestStopAccountsForEventsWhenSpoolIsUnavailable() {
  Drain();
  const auto unique = std::chrono::steady_clock::now()
                          .time_since_epoch()
                          .count();
  const fs::path root = fs::canonical(fs::temp_directory_path()) /
                        ("yunpin-native-spool-stop-" +
                         std::to_string(unique));
#if defined(_WIN32)
  const fs::path incoming =
      root / "YunPinIME" / "sync" / "native-events" / "incoming";
#else
  const fs::path incoming =
      root / "YunPin" / "Sync" / "native-events" / "incoming";
#endif
  PrepareEmptyPrivateSpool(incoming);
  // An unexpected non-regular entry makes the directory fail closed and
  // forces Stop() to account for the pending queue via its drop counter.
  fs::create_directory(incoming / "unsafe-entry");
  const std::uint64_t spool_dropped_before =
      YunPinNativeSelectionSpoolDropCount();
  const std::uint64_t queue_dropped_before =
      NativeSelectionEventQueue::Instance().dropped();
  constexpr std::size_t kEvents = 32;
  std::size_t accepted = 0;
  NativeSelectionEventQueue::Instance().ResumePublishingForSpoolerStart();
  for (std::size_t index = 0; index < kEvents; ++index) {
    if (NativeSelectionEventQueue::Instance().TryPublish("停止", "ting zhi")) {
      ++accepted;
    }
  }
  const std::string incoming_utf8 = incoming.u8string();
  assert(YunPinStartNativeSelectionSpoolerV1(incoming_utf8.c_str()));
  YunPinStopNativeSelectionSpoolerV1();
  const std::uint64_t spool_dropped_after =
      YunPinNativeSelectionSpoolDropCount();
  const std::uint64_t queue_dropped_after =
      NativeSelectionEventQueue::Instance().dropped();
  std::size_t spooled = 0;
  if (fs::is_directory(incoming)) {
    for (const auto& entry : fs::directory_iterator(incoming)) {
      if (entry.path().extension() == ".json") {
        ++spooled;
      }
    }
  }
  assert(spooled + (spool_dropped_after - spool_dropped_before) == accepted);
  assert(accepted + (queue_dropped_after - queue_dropped_before) == kEvents);
  assert(NativeSelectionEventQueue::Instance().size() == 0);
#if !defined(_WIN32)
  fs::permissions(incoming, fs::perms::owner_all, fs::perm_options::replace);
#endif
  fs::remove_all(root);
}

void TestConcurrentPublishDuringStopIsFullyAccounted() {
  Drain();
  const auto unique = std::chrono::steady_clock::now()
                          .time_since_epoch()
                          .count();
  const fs::path root = fs::canonical(fs::temp_directory_path()) /
                        ("yunpin-native-spool-stop-race-" +
                         std::to_string(unique));
#if defined(_WIN32)
  const fs::path incoming =
      root / "YunPinIME" / "sync" / "native-events" / "incoming";
#else
  const fs::path incoming =
      root / "YunPin" / "Sync" / "native-events" / "incoming";
#endif
  PrepareEmptyPrivateSpool(incoming);
  fs::create_directory(incoming / "unsafe-entry");
  const std::uint64_t spool_dropped_before =
      YunPinNativeSelectionSpoolDropCount();
  const std::uint64_t queue_dropped_before =
      NativeSelectionEventQueue::Instance().dropped();
  const std::string incoming_utf8 = incoming.u8string();
  assert(YunPinStartNativeSelectionSpoolerV1(incoming_utf8.c_str()));

  constexpr std::size_t kAttempts = 4096;
  std::atomic<bool> running{false};
  std::atomic<std::size_t> accepted{0};
  std::thread publisher([&] {
    running.store(true, std::memory_order_release);
    for (std::size_t index = 0; index < kAttempts; ++index) {
      if (NativeSelectionEventQueue::Instance().TryPublish("竞态", "jing tai")) {
        accepted.fetch_add(1, std::memory_order_relaxed);
      }
    }
  });
  while (!running.load(std::memory_order_acquire)) {
    std::this_thread::yield();
  }
  YunPinStopNativeSelectionSpoolerV1();
  publisher.join();

  const std::uint64_t spool_dropped =
      YunPinNativeSelectionSpoolDropCount() - spool_dropped_before;
  const std::uint64_t queue_dropped =
      NativeSelectionEventQueue::Instance().dropped() - queue_dropped_before;
  assert(accepted.load(std::memory_order_relaxed) + queue_dropped == kAttempts);
  assert(spool_dropped == accepted.load(std::memory_order_relaxed));
  assert(NativeSelectionEventQueue::Instance().size() == 0);
  fs::remove_all(root);
}

}  // namespace

int main(int argc, char** argv) {
  if (argc == 5 && std::string_view(argv[1]) == "--spool-child") {
    return RunCrossProcessSpoolChild(argv[2], argv[3], argv[4]);
  }
  TestRoundTripAndUniqueIds();
  TestInvalidAndOversizedValuesFailClosed();
  TestBoundedQueueDropsWithoutOverwriting();
  TestStableCBoundary();
  TestAtomicPrivateSpoolAndLifecycle();
  TestSpoolQuotaDropsWithoutBlockingOrGrowing();
  TestRecoversDurableTempBeforeWritingNextEvent();
  assert(argc >= 1);
  TestCrossProcessProducersRespectSharedQuota(fs::canonical(argv[0]));
  for (int cycle = 0; cycle < 16; ++cycle) {
    TestStopAccountsForEventsWhenSpoolIsUnavailable();
  }
  for (int cycle = 0; cycle < 4; ++cycle) {
    TestConcurrentPublishDuringStopIsFullyAccounted();
  }
  Drain();
  return 0;
}
