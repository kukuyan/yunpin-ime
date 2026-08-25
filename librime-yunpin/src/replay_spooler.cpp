// SPDX-License-Identifier: Apache-2.0
#include "yunpin/replay_native.hpp"

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <thread>

#if defined(_WIN32)
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <shlobj.h>
#include <windows.h>
#else
#include <cerrno>
#include <fcntl.h>
#include <sys/file.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>
#endif

namespace yunpin {
namespace {

namespace fs = std::filesystem;

constexpr std::uintmax_t kMaxActiveMetadataBytes = 64 * 1024;
constexpr std::uintmax_t kMaxNativeSessionBytes = 64 * 1024 * 1024;
constexpr auto kReplayPollInterval = std::chrono::milliseconds(50);

struct ActiveReplaySession {
  std::string session_id;
};

bool HasExpectedReplayRootSuffix(const fs::path& path) noexcept {
  try {
#if defined(_WIN32)
    return path.filename() == L"ReplayLab" &&
           path.parent_path().filename() == L"YunPin";
#else
    return path.filename() == "ReplayLab" &&
           path.parent_path().filename() == "YunPin";
#endif
  } catch (...) {
    return false;
  }
}

bool IsReplaySessionId(std::string_view value) noexcept {
  if (value.size() != 32) {
    return false;
  }
  for (const unsigned char byte : value) {
    if (!((byte >= '0' && byte <= '9') ||
          (byte >= 'a' && byte <= 'f'))) {
      return false;
    }
  }
  return true;
}

std::optional<std::string> SimpleJsonStringField(
    std::string_view json, std::string_view key) noexcept {
  try {
    const std::string needle = "\"" + std::string(key) + "\"";
    const std::size_t field = json.find(needle);
    if (field == std::string_view::npos ||
        json.find(needle, field + needle.size()) != std::string_view::npos) {
      return std::nullopt;
    }
    std::size_t cursor = field + needle.size();
    while (cursor < json.size() &&
           (json[cursor] == ' ' || json[cursor] == '\t' ||
            json[cursor] == '\r' || json[cursor] == '\n')) {
      ++cursor;
    }
    if (cursor >= json.size() || json[cursor++] != ':') {
      return std::nullopt;
    }
    while (cursor < json.size() &&
           (json[cursor] == ' ' || json[cursor] == '\t' ||
            json[cursor] == '\r' || json[cursor] == '\n')) {
      ++cursor;
    }
    if (cursor >= json.size() || json[cursor++] != '"') {
      return std::nullopt;
    }
    const std::size_t start = cursor;
    while (cursor < json.size() && json[cursor] != '"') {
      const unsigned char byte = static_cast<unsigned char>(json[cursor]);
      if (byte < 0x20 || json[cursor] == '\\') {
        return std::nullopt;
      }
      ++cursor;
    }
    if (cursor >= json.size()) {
      return std::nullopt;
    }
    return std::string(json.substr(start, cursor - start));
  } catch (...) {
    return std::nullopt;
  }
}

#if defined(_WIN32)

bool IsSafeWindowsReplayRoot(const fs::path& input,
                             fs::path* normalized) noexcept {
  try {
    if (!normalized || input.empty() || !input.is_absolute() ||
        input == input.root_path() ||
        input.native().find(L'\0') != std::wstring::npos) {
      return false;
    }
    *normalized = input.lexically_normal();
    if (!HasExpectedReplayRootSuffix(*normalized)) {
      return false;
    }
    for (const fs::path& component : normalized->relative_path()) {
      const std::wstring value = component.native();
      if (value.empty() || value == L"." || value == L".." ||
          value.back() == L'.' || value.back() == L' ') {
        return false;
      }
      for (const wchar_t character : value) {
        if (character < 0x20 || character == L'<' || character == L'>' ||
            character == L':' || character == L'"' || character == L'|' ||
            character == L'?' || character == L'*' ||
            character == L'\\' || character == L'/') {
          return false;
        }
      }
    }
    return true;
  } catch (...) {
    return false;
  }
}

bool IsWindowsPathObjectSafe(const fs::path& path,
                             bool directory) noexcept {
  HANDLE handle = CreateFileW(
      path.c_str(), FILE_READ_ATTRIBUTES,
      FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, nullptr,
      OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS,
      nullptr);
  if (handle == INVALID_HANDLE_VALUE) {
    return false;
  }
  BY_HANDLE_FILE_INFORMATION info{};
  const bool valid = GetFileInformationByHandle(handle, &info) != 0 &&
                     (info.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT) ==
                         0 &&
                     ((info.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) !=
                      0) == directory;
  (void)CloseHandle(handle);
  return valid;
}

std::optional<std::string> ReadBoundedReplayFile(
    const fs::path& path) noexcept {
  HANDLE file = CreateFileW(
      path.c_str(), GENERIC_READ,
      FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, nullptr,
      OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_ATTRIBUTE_NORMAL,
      nullptr);
  if (file == INVALID_HANDLE_VALUE) {
    return std::nullopt;
  }
  BY_HANDLE_FILE_INFORMATION info{};
  LARGE_INTEGER size{};
  const bool safe = GetFileInformationByHandle(file, &info) != 0 &&
                    (info.dwFileAttributes &
                     (FILE_ATTRIBUTE_REPARSE_POINT |
                      FILE_ATTRIBUTE_DIRECTORY)) == 0 &&
                    GetFileSizeEx(file, &size) != 0 && size.QuadPart > 0 &&
                    static_cast<std::uint64_t>(size.QuadPart) <=
                        kMaxActiveMetadataBytes;
  if (!safe) {
    (void)CloseHandle(file);
    return std::nullopt;
  }
  try {
    std::string contents(static_cast<std::size_t>(size.QuadPart), '\0');
    DWORD offset = 0;
    while (offset < contents.size()) {
      DWORD read = 0;
      const DWORD remaining =
          static_cast<DWORD>(contents.size() - offset);
      if (!ReadFile(file, contents.data() + offset, remaining, &read,
                    nullptr) || read == 0) {
        (void)CloseHandle(file);
        return std::nullopt;
      }
      offset += read;
    }
    char extra = '\0';
    DWORD extra_read = 0;
    const bool complete = ReadFile(file, &extra, 1, &extra_read, nullptr) != 0 &&
                          extra_read == 0;
    (void)CloseHandle(file);
    return complete ? std::optional<std::string>(std::move(contents))
                    : std::nullopt;
  } catch (...) {
    (void)CloseHandle(file);
    return std::nullopt;
  }
}

bool EnsureReplayNativeDirectory(const fs::path& root,
                                 fs::path* native) noexcept {
  try {
    if (!native || !IsWindowsPathObjectSafe(root, true)) {
      return false;
    }
    *native = root / L"native";
    if (!CreateDirectoryW(native->c_str(), nullptr) &&
        GetLastError() != ERROR_ALREADY_EXISTS) {
      return false;
    }
    return IsWindowsPathObjectSafe(*native, true);
  } catch (...) {
    return false;
  }
}

bool AppendReplayLine(const fs::path& path, std::string_view line) noexcept {
  if (line.empty() || line.size() > kReplayJsonLimit) {
    return false;
  }
  HANDLE file = CreateFileW(
      path.c_str(), FILE_APPEND_DATA | FILE_READ_ATTRIBUTES, FILE_SHARE_READ,
      nullptr, OPEN_ALWAYS,
      FILE_ATTRIBUTE_NORMAL | FILE_FLAG_OPEN_REPARSE_POINT |
          FILE_FLAG_WRITE_THROUGH,
      nullptr);
  if (file == INVALID_HANDLE_VALUE) {
    return false;
  }
  BY_HANDLE_FILE_INFORMATION info{};
  LARGE_INTEGER size{};
  bool safe = GetFileInformationByHandle(file, &info) != 0 &&
              (info.dwFileAttributes &
               (FILE_ATTRIBUTE_REPARSE_POINT | FILE_ATTRIBUTE_DIRECTORY)) ==
                  0 &&
              GetFileSizeEx(file, &size) != 0 && size.QuadPart >= 0 &&
              static_cast<std::uint64_t>(size.QuadPart) <=
                  kMaxNativeSessionBytes - line.size() - 1;
  std::string record;
  try {
    record.assign(line);
    record.push_back('\n');
  } catch (...) {
    safe = false;
  }
  DWORD written = 0;
  const bool durable =
      safe && WriteFile(file, record.data(), static_cast<DWORD>(record.size()),
                        &written, nullptr) != 0 &&
      written == record.size() && FlushFileBuffers(file) != 0;
  (void)CloseHandle(file);
  return durable;
}

std::optional<fs::path> DefaultWindowsReplayRoot() noexcept {
  PWSTR local_app_data = nullptr;
  const HRESULT result = SHGetKnownFolderPath(
      FOLDERID_LocalAppData, KF_FLAG_CREATE | KF_FLAG_NO_ALIAS, nullptr,
      &local_app_data);
  if (FAILED(result) || !local_app_data) {
    if (local_app_data) {
      CoTaskMemFree(local_app_data);
    }
    return std::nullopt;
  }
  try {
    const fs::path requested =
        fs::path(local_app_data) / L"YunPin" / L"ReplayLab";
    CoTaskMemFree(local_app_data);
    local_app_data = nullptr;
    fs::path normalized;
    return IsSafeWindowsReplayRoot(requested, &normalized)
               ? std::optional<fs::path>(std::move(normalized))
               : std::nullopt;
  } catch (...) {
    if (local_app_data) {
      CoTaskMemFree(local_app_data);
    }
    return std::nullopt;
  }
}

#else

bool IsSafeUnixReplayRoot(const fs::path& input,
                          fs::path* normalized) noexcept {
  try {
    if (!normalized || input.empty() || !input.is_absolute() ||
        input == input.root_path() ||
        input.native().find('\0') != std::string::npos) {
      return false;
    }
    *normalized = input.lexically_normal();
    if (!HasExpectedReplayRootSuffix(*normalized)) {
      return false;
    }
    for (const fs::path& component : normalized->relative_path()) {
      if (component.empty() || component == fs::path(".") ||
          component == fs::path("..")) {
        return false;
      }
    }
    return true;
  } catch (...) {
    return false;
  }
}

bool IsOwnedDirectory(const fs::path& path) noexcept {
  struct stat status {};
  return ::lstat(path.c_str(), &status) == 0 && S_ISDIR(status.st_mode) &&
         status.st_uid == ::geteuid();
}

std::optional<std::string> ReadBoundedReplayFile(
    const fs::path& path) noexcept {
  int flags = O_RDONLY | O_CLOEXEC;
#if defined(O_NOFOLLOW)
  flags |= O_NOFOLLOW;
#endif
  const int file = ::open(path.c_str(), flags);
  if (file < 0) {
    return std::nullopt;
  }
  struct stat opened {};
  struct stat named {};
  if (::fstat(file, &opened) != 0 || ::lstat(path.c_str(), &named) != 0 ||
      !S_ISREG(opened.st_mode) || !S_ISREG(named.st_mode) ||
      opened.st_uid != ::geteuid() || named.st_uid != ::geteuid() ||
      opened.st_nlink != 1 || named.st_nlink != 1 ||
      opened.st_dev != named.st_dev || opened.st_ino != named.st_ino ||
      opened.st_size != named.st_size || opened.st_size <= 0 ||
      static_cast<std::uintmax_t>(opened.st_size) >
          kMaxActiveMetadataBytes) {
    (void)::close(file);
    return std::nullopt;
  }
  try {
    std::string contents(static_cast<std::size_t>(opened.st_size), '\0');
    std::size_t offset = 0;
    while (offset < contents.size()) {
      const ssize_t count =
          ::read(file, contents.data() + offset, contents.size() - offset);
      if (count < 0 && errno == EINTR) {
        continue;
      }
      if (count <= 0) {
        (void)::close(file);
        return std::nullopt;
      }
      offset += static_cast<std::size_t>(count);
    }
    char extra = '\0';
    const ssize_t extra_read = ::read(file, &extra, 1);
    (void)::close(file);
    return extra_read == 0
               ? std::optional<std::string>(std::move(contents))
               : std::nullopt;
  } catch (...) {
    (void)::close(file);
    return std::nullopt;
  }
}

bool EnsureReplayNativeDirectory(const fs::path& root,
                                 fs::path* native) noexcept {
  try {
    if (!native || !IsOwnedDirectory(root) || ::chmod(root.c_str(), 0700) != 0) {
      return false;
    }
    *native = root / "native";
    if (::mkdir(native->c_str(), 0700) != 0 && errno != EEXIST) {
      return false;
    }
    return IsOwnedDirectory(*native) && ::chmod(native->c_str(), 0700) == 0;
  } catch (...) {
    return false;
  }
}

bool WriteAll(int file, std::string_view value) noexcept {
  std::size_t offset = 0;
  while (offset < value.size()) {
    const ssize_t written =
        ::write(file, value.data() + offset, value.size() - offset);
    if (written < 0 && errno == EINTR) {
      continue;
    }
    if (written <= 0) {
      return false;
    }
    offset += static_cast<std::size_t>(written);
  }
  return true;
}

bool AppendReplayLine(const fs::path& path, std::string_view line) noexcept {
  if (line.empty() || line.size() > kReplayJsonLimit) {
    return false;
  }
  int flags = O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC;
#if defined(O_NOFOLLOW)
  flags |= O_NOFOLLOW;
#endif
  const int file = ::open(path.c_str(), flags, 0600);
  if (file < 0) {
    return false;
  }
  struct stat opened {};
  struct stat named {};
  const bool safe = ::flock(file, LOCK_EX) == 0 &&
                    ::fchmod(file, 0600) == 0 &&
                    ::fstat(file, &opened) == 0 &&
                    ::lstat(path.c_str(), &named) == 0 &&
                    S_ISREG(opened.st_mode) && S_ISREG(named.st_mode) &&
                    opened.st_uid == ::geteuid() &&
                    named.st_uid == ::geteuid() && opened.st_nlink == 1 &&
                    named.st_nlink == 1 && opened.st_dev == named.st_dev &&
                    opened.st_ino == named.st_ino && opened.st_size >= 0 &&
                    static_cast<std::uintmax_t>(opened.st_size) <=
                        kMaxNativeSessionBytes - line.size() - 1;
  const bool durable = safe && WriteAll(file, line) &&
                       WriteAll(file, std::string_view("\n", 1)) &&
                       ::fsync(file) == 0;
  (void)::flock(file, LOCK_UN);
  (void)::close(file);
  return durable;
}

#endif

std::optional<ActiveReplaySession> ReadActiveSession(
    const fs::path& root) noexcept {
  try {
#if defined(_WIN32)
    if (!IsWindowsPathObjectSafe(root, true)) {
      return std::nullopt;
    }
#else
    if (!IsOwnedDirectory(root)) {
      return std::nullopt;
    }
#endif
    const auto contents = ReadBoundedReplayFile(root / "active.json");
    if (!contents) {
      return std::nullopt;
    }
    const auto version = SimpleJsonStringField(*contents, "version");
    const auto state = SimpleJsonStringField(*contents, "state");
    const auto session = SimpleJsonStringField(*contents, "session_id");
    if (!version || *version != "yunpin.replay.session.v1" || !state ||
        *state != "running" || !session || !IsReplaySessionId(*session)) {
      return std::nullopt;
    }
    return ActiveReplaySession{*session};
  } catch (...) {
    return std::nullopt;
  }
}

class ReplaySpooler {
 public:
  static ReplaySpooler& Instance() {
    static ReplaySpooler spooler;
    return spooler;
  }

  ~ReplaySpooler() { Stop(); }

  bool Start(std::string_view absolute_utf8_root) noexcept {
    if (absolute_utf8_root.empty()) {
      return false;
    }
    fs::path root;
    try {
      const fs::path requested = fs::u8path(absolute_utf8_root);
#if defined(_WIN32)
      if (!IsSafeWindowsReplayRoot(requested, &root)) {
        return false;
      }
#else
      if (!IsSafeUnixReplayRoot(requested, &root)) {
        return false;
      }
#endif
    } catch (...) {
      return false;
    }

    std::lock_guard<std::mutex> lock(control_mutex_);
    if (stopping_) {
      return false;
    }
    if (worker_.joinable()) {
      return root == root_ && !stop_requested_;
    }
    root_ = std::move(root);
    stop_requested_ = false;
    try {
      // Keep the producer disabled until the worker verifies an explicitly
      // running Replay Lab session. Starting the input method alone therefore
      // records no text.
      GlobalReplayNativeProducer().SetEnabled(false);
      worker_ = std::thread(&ReplaySpooler::Run, this, root_);
    } catch (...) {
      root_.clear();
      return false;
    }
    return true;
  }

  void Stop() noexcept {
    std::thread worker;
    {
      std::lock_guard<std::mutex> lock(control_mutex_);
      if (!worker_.joinable() || stopping_) {
        GlobalReplayNativeProducer().SetEnabled(false);
        return;
      }
      stopping_ = true;
      stop_requested_ = true;
      GlobalReplayNativeProducer().SetEnabled(false);
      worker = std::move(worker_);
    }
    wakeup_.notify_all();
    if (worker.joinable()) {
      worker.join();
    }
    (void)GlobalReplayNativeProducer().DiscardAll();
    {
      std::lock_guard<std::mutex> lock(control_mutex_);
      root_.clear();
      stop_requested_ = false;
      stopping_ = false;
    }
  }

  std::uint64_t dropped() const noexcept {
    return dropped_.load(std::memory_order_relaxed);
  }

 private:
  ReplaySpooler() = default;
  ReplaySpooler(const ReplaySpooler&) = delete;
  ReplaySpooler& operator=(const ReplaySpooler&) = delete;

  bool StopRequested() noexcept {
    std::lock_guard<std::mutex> lock(control_mutex_);
    return stop_requested_;
  }

  void Wait() noexcept {
    std::unique_lock<std::mutex> lock(control_mutex_);
    if (!stop_requested_) {
      wakeup_.wait_for(lock, kReplayPollInterval);
    }
  }

  void DisableAndDiscard() noexcept {
    auto& producer = GlobalReplayNativeProducer();
    producer.SetEnabled(false);
    (void)producer.DiscardAll();
  }

  void Run(fs::path root) noexcept {
    std::string active_session;
    fs::path native_directory;
    while (!StopRequested()) {
      const auto active = ReadActiveSession(root);
      if (!active) {
        DisableAndDiscard();
        active_session.clear();
        native_directory.clear();
        Wait();
        continue;
      }
      if (active->session_id != active_session) {
        DisableAndDiscard();
        fs::path candidate_directory;
        if (!EnsureReplayNativeDirectory(root, &candidate_directory)) {
          active_session.clear();
          native_directory.clear();
          Wait();
          continue;
        }
        active_session = active->session_id;
        native_directory = std::move(candidate_directory);
        GlobalReplayNativeProducer().SetEnabled(true);
      }

      char json[kReplayJsonLimit + 1]{};
      const std::size_t size =
          GlobalReplayNativeProducer().DrainJson(json, sizeof(json));
      if (size == 0) {
        Wait();
        continue;
      }
      const fs::path output = native_directory /
                              fs::u8path(active_session +
                                         ".native.yunpinreplay");
      if (!AppendReplayLine(output, std::string_view(json, size))) {
        dropped_.fetch_add(1, std::memory_order_relaxed);
      }
    }
    DisableAndDiscard();
  }

  mutable std::mutex control_mutex_;
  std::condition_variable wakeup_;
  std::thread worker_;
  fs::path root_;
  bool stop_requested_{false};
  bool stopping_{false};
  std::atomic<std::uint64_t> dropped_{0};
};

}  // namespace
}  // namespace yunpin

extern "C" bool YunPinStartReplaySpoolerV1(
    const char* absolute_utf8_root) noexcept {
  if (!absolute_utf8_root) {
    return false;
  }
  try {
    (void)yunpin::GlobalReplayNativeProducer();
    return yunpin::ReplaySpooler::Instance().Start(absolute_utf8_root);
  } catch (...) {
    return false;
  }
}

extern "C" bool YunPinStartDefaultReplaySpoolerV1() noexcept {
#if defined(_WIN32)
  try {
    const auto root = yunpin::DefaultWindowsReplayRoot();
    if (!root) {
      return false;
    }
    const std::string utf8 = root->u8string();
    return YunPinStartReplaySpoolerV1(utf8.c_str());
  } catch (...) {
    return false;
  }
#else
  return false;
#endif
}

extern "C" void YunPinStopReplaySpoolerV1() noexcept {
  try {
    (void)yunpin::GlobalReplayNativeProducer();
    yunpin::ReplaySpooler::Instance().Stop();
  } catch (...) {
  }
}

extern "C" std::uint64_t YunPinReplaySpoolDropCountV1() noexcept {
  try {
    return yunpin::ReplaySpooler::Instance().dropped();
  } catch (...) {
    return 0;
  }
}
