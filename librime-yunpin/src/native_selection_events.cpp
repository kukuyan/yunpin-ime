// SPDX-License-Identifier: Apache-2.0
#include "yunpin/native_selection_events.hpp"

#include <array>
#include <atomic>
#include <charconv>
#include <chrono>
#include <condition_variable>
#include <cstring>
#include <filesystem>
#include <memory>
#include <mutex>
#include <optional>
#include <random>
#include <string>
#include <thread>
#include <utility>
#include <vector>

#if defined(_WIN32)
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <aclapi.h>
#include <shlobj.h>
#include <windows.h>
#else
#include <cerrno>
#include <fcntl.h>
#include <stdio.h>
#include <sys/file.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>
#endif

namespace yunpin {
namespace {

namespace fs = std::filesystem;

constexpr std::size_t kMaxSerializedEventBytes = 4096;
constexpr std::size_t kMaxSpoolEntries = 2048;
constexpr std::uintmax_t kMaxSpoolBytes = 8 * 1024 * 1024;
constexpr std::size_t kMaxStopDrainEvents = 64;

void NotifyNativeSelectionSpooler() noexcept;

bool IsAsciiPinyin(std::string_view value) noexcept {
  if (value.empty() || value.size() > NativeSelectionEventQueue::kMaxPinyinBytes) {
    return false;
  }
  for (const unsigned char byte : value) {
    if (!((byte >= 'a' && byte <= 'z') || byte == ' ' || byte == '\'')) {
      return false;
    }
  }
  return true;
}

bool IsSafeUtf8(std::string_view value) noexcept {
  if (value.empty() || value.size() > NativeSelectionEventQueue::kMaxPhraseBytes) {
    return false;
  }
  for (std::size_t offset = 0; offset < value.size();) {
    const unsigned char first = static_cast<unsigned char>(value[offset]);
    std::uint32_t codepoint = 0;
    std::size_t width = 0;
    std::uint32_t minimum = 0;
    if (first < 0x80) {
      codepoint = first;
      width = 1;
    } else if ((first & 0xe0) == 0xc0) {
      codepoint = first & 0x1f;
      width = 2;
      minimum = 0x80;
    } else if ((first & 0xf0) == 0xe0) {
      codepoint = first & 0x0f;
      width = 3;
      minimum = 0x800;
    } else if ((first & 0xf8) == 0xf0) {
      codepoint = first & 0x07;
      width = 4;
      minimum = 0x10000;
    } else {
      return false;
    }
    if (offset + width > value.size()) {
      return false;
    }
    for (std::size_t index = 1; index < width; ++index) {
      const unsigned char continuation =
          static_cast<unsigned char>(value[offset + index]);
      if ((continuation & 0xc0) != 0x80) {
        return false;
      }
      codepoint = (codepoint << 6) | (continuation & 0x3f);
    }
    const bool invalid =
        (width > 1 && codepoint < minimum) || codepoint > 0x10ffff ||
        (codepoint >= 0xd800 && codepoint <= 0xdfff) ||
        codepoint < 0x20 || codepoint == 0x7f ||
        (codepoint >= 0x202a && codepoint <= 0x202e) ||
        (codepoint >= 0x2066 && codepoint <= 0x2069);
    if (invalid) {
      return false;
    }
    offset += width;
  }
  return true;
}

std::string RandomProcessPrefix() {
  std::array<std::uint32_t, 4> words{};
  try {
    std::random_device random;
    for (auto& word : words) {
      word = random();
    }
  } catch (...) {
    // This is a uniqueness token rather than an encryption key.  A clock and
    // object address still provide a per-process fallback on platforms whose
    // random_device implementation is unavailable.
    const auto ticks = static_cast<std::uint64_t>(
        std::chrono::steady_clock::now().time_since_epoch().count());
    const auto address = reinterpret_cast<std::uintptr_t>(&words);
    words = {static_cast<std::uint32_t>(ticks),
             static_cast<std::uint32_t>(ticks >> 32),
             static_cast<std::uint32_t>(address),
             static_cast<std::uint32_t>(address >> 32)};
  }
  constexpr char digits[] = "0123456789abcdef";
  std::string output(32, '0');
  std::size_t offset = 0;
  for (const std::uint32_t word : words) {
    for (int shift = 28; shift >= 0; shift -= 4) {
      output[offset++] = digits[(word >> shift) & 0x0f];
    }
  }
  return output;
}

bool IsSafeEventId(std::string_view value) noexcept {
  if (value.empty() || value.size() > NativeSelectionEventQueue::kMaxEventIdBytes) {
    return false;
  }
  for (const unsigned char byte : value) {
    if (!((byte >= 'a' && byte <= 'z') ||
          (byte >= 'A' && byte <= 'Z') ||
          (byte >= '0' && byte <= '9') || byte == '_' || byte == '-')) {
      return false;
    }
  }
  return true;
}

void AppendJsonString(std::string_view value, std::string* output) {
  output->push_back('"');
  for (const char byte : value) {
    if (byte == '"' || byte == '\\') {
      output->push_back('\\');
    }
    output->push_back(byte);
  }
  output->push_back('"');
}

std::optional<std::string> SerializeEvent(
    const NativeSelectionEvent& event) noexcept {
  if (!IsSafeEventId(event.event_id) || !IsSafeUtf8(event.phrase) ||
      !IsAsciiPinyin(event.pinyin)) {
    return std::nullopt;
  }
  try {
    std::string output;
    output.reserve(event.event_id.size() + event.phrase.size() +
                   event.pinyin.size() + 64);
    output.append("{\"version\":1,\"event_id\":");
    AppendJsonString(event.event_id, &output);
    output.append(",\"phrase\":");
    AppendJsonString(event.phrase, &output);
    output.append(",\"pinyin\":");
    AppendJsonString(event.pinyin, &output);
    output.append("}\n");
    if (output.size() > kMaxSerializedEventBytes) {
      return std::nullopt;
    }
    return output;
  } catch (...) {
    return std::nullopt;
  }
}

bool ConsumeLiteral(std::string_view input,
                    std::size_t* cursor,
                    std::string_view literal) noexcept {
  if (!cursor || *cursor > input.size() ||
      input.size() - *cursor < literal.size() ||
      input.substr(*cursor, literal.size()) != literal) {
    return false;
  }
  *cursor += literal.size();
  return true;
}

bool ConsumeCanonicalJsonString(std::string_view input,
                                std::size_t* cursor,
                                std::string* output) {
  if (!cursor || !output || *cursor >= input.size() ||
      input[*cursor] != '"') {
    return false;
  }
  ++*cursor;
  while (*cursor < input.size()) {
    const char byte = input[(*cursor)++];
    if (byte == '"') {
      return true;
    }
    if (byte == '\\') {
      if (*cursor >= input.size()) {
        return false;
      }
      const char escaped = input[(*cursor)++];
      if (escaped != '"' && escaped != '\\') {
        return false;
      }
      output->push_back(escaped);
      continue;
    }
    output->push_back(byte);
  }
  return false;
}

std::optional<NativeSelectionEvent> ParseSerializedEvent(
    std::string_view input) noexcept {
  if (input.empty() || input.size() > kMaxSerializedEventBytes) {
    return std::nullopt;
  }
  try {
    std::size_t cursor = 0;
    NativeSelectionEvent event;
    if (!ConsumeLiteral(input, &cursor,
                        "{\"version\":1,\"event_id\":") ||
        !ConsumeCanonicalJsonString(input, &cursor, &event.event_id) ||
        !ConsumeLiteral(input, &cursor, ",\"phrase\":") ||
        !ConsumeCanonicalJsonString(input, &cursor, &event.phrase) ||
        !ConsumeLiteral(input, &cursor, ",\"pinyin\":") ||
        !ConsumeCanonicalJsonString(input, &cursor, &event.pinyin) ||
        !ConsumeLiteral(input, &cursor, "}\n") || cursor != input.size()) {
      return std::nullopt;
    }
    const auto canonical = SerializeEvent(event);
    if (!canonical || *canonical != input) {
      return std::nullopt;
    }
    return event;
  } catch (...) {
    return std::nullopt;
  }
}

bool IsLowerHex(std::string_view value) noexcept {
  if (value.empty() || value.size() > 16) {
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

bool IsExpectedTempName(std::string_view filename,
                        std::string_view event_id) noexcept {
  constexpr std::string_view prefix = ".tmp-";
  if (filename.size() <= prefix.size() + event_id.size() + 1 ||
      filename.substr(0, prefix.size()) != prefix ||
      filename.substr(prefix.size(), event_id.size()) != event_id ||
      filename[prefix.size() + event_id.size()] != '-') {
    return false;
  }
  return IsLowerHex(filename.substr(prefix.size() + event_id.size() + 1));
}

std::string ExpectedEventJson(const NativeSelectionEvent& event) {
  const auto serialized = SerializeEvent(event);
  return serialized ? *serialized : std::string();
}

std::optional<std::string> ReadPrivateBoundedFile(
    const fs::path& path) noexcept;

bool FileContainsExpectedEvent(const fs::path& path,
                               const NativeSelectionEvent& event) noexcept {
  try {
    const std::string expected = ExpectedEventJson(event);
    if (expected.empty()) {
      return false;
    }
    const auto actual = ReadPrivateBoundedFile(path);
    return actual && *actual == expected;
  } catch (...) {
    return false;
  }
}

#if defined(_WIN32)

bool HasExpectedSpoolSuffix(const fs::path& path) {
  return path.filename() == L"incoming" &&
         path.parent_path().filename() == L"native-events" &&
         path.parent_path().parent_path().filename() == L"sync" &&
         path.parent_path().parent_path().parent_path().filename() ==
             L"YunPinIME";
}

constexpr ACCESS_MASK kPrivateWindowsFullControl = 0x001f01ff;
constexpr BYTE kPrivateWindowsDirectoryAceFlags =
    OBJECT_INHERIT_ACE | CONTAINER_INHERIT_ACE;

struct WindowsObjectIdentity {
  DWORD volume_serial{0};
  DWORD file_index_high{0};
  DWORD file_index_low{0};
  DWORD creation_high{0};
  DWORD creation_low{0};
  bool directory{false};

  bool operator==(const WindowsObjectIdentity& other) const noexcept {
    return volume_serial == other.volume_serial &&
           file_index_high == other.file_index_high &&
           file_index_low == other.file_index_low &&
           creation_high == other.creation_high &&
           creation_low == other.creation_low &&
           directory == other.directory;
  }
};

struct WindowsPathComponent {
  fs::path path;
  WindowsObjectIdentity identity;
};

bool IsWindowsSlash(wchar_t value) noexcept {
  return value == L'\\' || value == L'/';
}

bool HasWindowsDeviceNamespace(std::wstring_view value) noexcept {
  return value.size() >= 4 && IsWindowsSlash(value[0]) &&
         IsWindowsSlash(value[1]) &&
         (value[2] == L'?' || value[2] == L'.') &&
         IsWindowsSlash(value[3]);
}

bool IsSafeWindowsPathComponent(std::wstring_view value) noexcept {
  if (value.empty() || value == L"." || value == L".." ||
      value.back() == L'.' || value.back() == L' ') {
    return false;
  }
  for (const wchar_t character : value) {
    if (character < 0x20 || character == L'<' || character == L'>' ||
        character == L':' || character == L'"' || character == L'|' ||
        character == L'?' || character == L'*' ||
        IsWindowsSlash(character)) {
      return false;
    }
  }
  std::wstring base(value.substr(0, value.find(L'.')));
  for (wchar_t& character : base) {
    if (character >= L'a' && character <= L'z') {
      character = static_cast<wchar_t>(character - L'a' + L'A');
    }
  }
  if (base == L"CON" || base == L"PRN" || base == L"AUX" ||
      base == L"NUL") {
    return false;
  }
  return !(base.size() == 4 &&
           (base.rfind(L"COM", 0) == 0 || base.rfind(L"LPT", 0) == 0) &&
           base[3] >= L'1' && base[3] <= L'9');
}

bool DecomposeWindowsPath(const fs::path& input,
                          fs::path* normalized,
                          std::vector<fs::path>* components) noexcept {
  try {
    if (!normalized || !components || input.empty() ||
        input.native().find(L'\0') != std::wstring::npos ||
        HasWindowsDeviceNamespace(input.native())) {
      return false;
    }
    for (const fs::path& component : input.relative_path()) {
      if (!IsSafeWindowsPathComponent(component.native())) {
        return false;
      }
    }
    *normalized = input.lexically_normal();
    const std::wstring root_name = normalized->root_name().native();
    if (!normalized->is_absolute() || normalized->root_directory().empty() ||
        root_name.size() != 2 || root_name[1] != L':' ||
        !((root_name[0] >= L'A' && root_name[0] <= L'Z') ||
          (root_name[0] >= L'a' && root_name[0] <= L'z'))) {
      return false;
    }
    components->clear();
    fs::path current = normalized->root_path();
    components->push_back(current);
    for (const fs::path& component : normalized->relative_path()) {
      if (!IsSafeWindowsPathComponent(component.native())) {
        return false;
      }
      current /= component;
      components->push_back(current);
    }
    return components->size() > 1 &&
           components->back().lexically_normal() == *normalized;
  } catch (...) {
    return false;
  }
}

HANDLE OpenWindowsPathNoReparse(
    const fs::path& path,
    DWORD access,
    DWORD share = FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
    DWORD disposition = OPEN_EXISTING,
    SECURITY_ATTRIBUTES* security = nullptr) noexcept {
  return CreateFileW(path.c_str(), access, share, security, disposition,
                     FILE_FLAG_OPEN_REPARSE_POINT |
                         FILE_FLAG_BACKUP_SEMANTICS,
                     nullptr);
}

bool WindowsIdentityForHandle(HANDLE handle,
                              WindowsObjectIdentity* identity) noexcept {
  BY_HANDLE_FILE_INFORMATION information{};
  if (handle == INVALID_HANDLE_VALUE || !identity ||
      !GetFileInformationByHandle(handle, &information) ||
      (information.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT) != 0) {
    return false;
  }
  *identity = WindowsObjectIdentity{
      information.dwVolumeSerialNumber,
      information.nFileIndexHigh,
      information.nFileIndexLow,
      information.ftCreationTime.dwHighDateTime,
      information.ftCreationTime.dwLowDateTime,
      (information.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) != 0};
  return true;
}

bool WindowsHandleMatchesPath(HANDLE handle,
                              const fs::path& expected) noexcept {
  try {
    std::vector<wchar_t> buffer(512);
    DWORD length = 0;
    while (true) {
      length = GetFinalPathNameByHandleW(handle, buffer.data(),
                                         static_cast<DWORD>(buffer.size()),
                                         FILE_NAME_NORMALIZED |
                                             VOLUME_NAME_DOS);
      if (length == 0 || length > 32768) {
        return false;
      }
      if (length < buffer.size()) {
        break;
      }
      buffer.resize(static_cast<std::size_t>(length) + 1);
    }
    std::wstring resolved(buffer.data(), length);
    constexpr std::wstring_view prefix = L"\\\\?\\";
    constexpr std::wstring_view unc_prefix = L"\\\\?\\UNC\\";
    if (resolved.size() >= unc_prefix.size() &&
        CompareStringOrdinal(resolved.data(),
                             static_cast<int>(unc_prefix.size()),
                             unc_prefix.data(),
                             static_cast<int>(unc_prefix.size()), TRUE) ==
            CSTR_EQUAL) {
      return false;
    }
    if (resolved.size() >= prefix.size() &&
        resolved.compare(0, prefix.size(), prefix) == 0) {
      resolved.erase(0, prefix.size());
    }
    const std::wstring expected_text = expected.lexically_normal().native();
    return CompareStringOrdinal(resolved.c_str(),
                                static_cast<int>(resolved.size()),
                                expected_text.c_str(),
                                static_cast<int>(expected_text.size()), TRUE) ==
           CSTR_EQUAL;
  } catch (...) {
    return false;
  }
}

bool InspectWindowsPathChain(
    const fs::path& input,
    bool target_directory,
    std::vector<WindowsPathComponent>* result) noexcept {
  fs::path normalized;
  std::vector<fs::path> components;
  if (!result || !DecomposeWindowsPath(input, &normalized, &components)) {
    return false;
  }
  try {
    result->clear();
    result->reserve(components.size());
    for (std::size_t index = 0; index < components.size(); ++index) {
      HANDLE handle = OpenWindowsPathNoReparse(
          components[index], FILE_READ_ATTRIBUTES);
      WindowsObjectIdentity identity;
      const bool valid = handle != INVALID_HANDLE_VALUE &&
                         WindowsIdentityForHandle(handle, &identity) &&
                         WindowsHandleMatchesPath(handle, components[index]) &&
                         identity.directory ==
                             (index + 1 < components.size() ||
                              target_directory);
      if (handle != INVALID_HANDLE_VALUE) {
        (void)CloseHandle(handle);
      }
      if (!valid) {
        return false;
      }
      result->push_back(WindowsPathComponent{components[index], identity});
    }
    return true;
  } catch (...) {
    return false;
  }
}

bool EqualWindowsPath(const fs::path& left,
                      const fs::path& right) noexcept {
  try {
    const std::wstring left_text = left.lexically_normal().native();
    const std::wstring right_text = right.lexically_normal().native();
    return CompareStringOrdinal(left_text.c_str(),
                                static_cast<int>(left_text.size()),
                                right_text.c_str(),
                                static_cast<int>(right_text.size()), TRUE) ==
           CSTR_EQUAL;
  } catch (...) {
    return false;
  }
}

bool SameWindowsPathChain(
    const std::vector<WindowsPathComponent>& left,
    const std::vector<WindowsPathComponent>& right) noexcept {
  if (left.size() != right.size()) {
    return false;
  }
  for (std::size_t index = 0; index < left.size(); ++index) {
    if (!EqualWindowsPath(left[index].path, right[index].path) ||
        !(left[index].identity == right[index].identity)) {
      return false;
    }
  }
  return true;
}

bool GetCurrentUserSid(std::vector<unsigned char>* storage,
                       PSID* sid) {
  if (!storage || !sid) {
    return false;
  }
  HANDLE token = nullptr;
  if (!OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &token)) {
    return false;
  }
  DWORD required = 0;
  (void)GetTokenInformation(token, TokenUser, nullptr, 0, &required);
  if (GetLastError() != ERROR_INSUFFICIENT_BUFFER || required == 0) {
    (void)CloseHandle(token);
    return false;
  }
  storage->resize(required);
  const bool ok = GetTokenInformation(token, TokenUser, storage->data(),
                                      required, &required) != 0;
  (void)CloseHandle(token);
  if (!ok) {
    return false;
  }
  *sid = reinterpret_cast<TOKEN_USER*>(storage->data())->User.Sid;
  return IsValidSid(*sid) != 0;
}

class PrivateWindowsSecurity {
 public:
  PrivateWindowsSecurity() {
    if (!GetCurrentUserSid(&sid_storage_, &user_sid_)) {
      return;
    }
    DWORD system_sid_size = static_cast<DWORD>(system_sid_storage_.size());
    system_sid_ = system_sid_storage_.data();
    if (!CreateWellKnownSid(WinLocalSystemSid, nullptr, system_sid_,
                            &system_sid_size) ||
        !BuildAcl(false, &file_acl_) ||
        !BuildAcl(true, &directory_acl_) ||
        !InitializeDescriptor(false, &file_descriptor_) ||
        !InitializeDescriptor(true, &directory_descriptor_)) {
      return;
    }
    file_attributes_ = {sizeof(file_attributes_), &file_descriptor_, FALSE};
    directory_attributes_ = {sizeof(directory_attributes_),
                             &directory_descriptor_, FALSE};
    ready_ = true;
  }

  ~PrivateWindowsSecurity() {
    if (file_acl_) {
      (void)LocalFree(file_acl_);
    }
    if (directory_acl_) {
      (void)LocalFree(directory_acl_);
    }
  }

  [[nodiscard]] bool ready() const noexcept { return ready_; }

  SECURITY_ATTRIBUTES* attributes(bool directory) noexcept {
    if (!ready_) {
      return nullptr;
    }
    return directory ? &directory_attributes_ : &file_attributes_;
  }

  bool ValidateHandle(HANDLE handle,
                      bool directory,
                      WindowsObjectIdentity* identity = nullptr) const noexcept {
    WindowsObjectIdentity observed;
    if (!ready_ || !WindowsIdentityForHandle(handle, &observed) ||
        observed.directory != directory) {
      return false;
    }
    PSID owner = nullptr;
    PACL dacl = nullptr;
    PSECURITY_DESCRIPTOR descriptor = nullptr;
    const DWORD result = GetSecurityInfo(
        handle, SE_FILE_OBJECT,
        OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION, &owner,
        nullptr, &dacl, nullptr, &descriptor);
    if (result != ERROR_SUCCESS || !descriptor || !owner || !dacl) {
      if (descriptor) {
        (void)LocalFree(descriptor);
      }
      return false;
    }
    PSID descriptor_owner = nullptr;
    BOOL owner_defaulted = TRUE;
    BOOL dacl_present = FALSE;
    BOOL dacl_defaulted = TRUE;
    PACL descriptor_dacl = nullptr;
    SECURITY_DESCRIPTOR_CONTROL control = 0;
    DWORD revision = 0;
    ACL_SIZE_INFORMATION information{};
    bool valid =
        GetSecurityDescriptorOwner(descriptor, &descriptor_owner,
                                   &owner_defaulted) != 0 &&
        descriptor_owner && !owner_defaulted &&
        EqualSid(descriptor_owner, user_sid_) != 0 &&
        EqualSid(owner, user_sid_) != 0 &&
        GetSecurityDescriptorControl(descriptor, &control, &revision) != 0 &&
        (control & SE_DACL_PRESENT) != 0 &&
        (control & SE_DACL_PROTECTED) != 0 &&
        (control & (SE_DACL_DEFAULTED | SE_DACL_AUTO_INHERIT_REQ)) == 0 &&
        GetSecurityDescriptorDacl(descriptor, &dacl_present,
                                  &descriptor_dacl, &dacl_defaulted) != 0 &&
        dacl_present && !dacl_defaulted && descriptor_dacl == dacl &&
        IsValidAcl(dacl) != 0 &&
        GetAclInformation(dacl, &information, sizeof(information),
                          AclSizeInformation) != 0 &&
        information.AceCount == 2;
    const BYTE expected_flags = directory
                                    ? kPrivateWindowsDirectoryAceFlags
                                    : static_cast<BYTE>(0);
    bool user_allowed = false;
    bool system_allowed = false;
    for (DWORD index = 0; valid && index < information.AceCount; ++index) {
      void* raw_ace = nullptr;
      if (!GetAce(dacl, index, &raw_ace) || !raw_ace) {
        valid = false;
        break;
      }
      const auto* ace = static_cast<const ACCESS_ALLOWED_ACE*>(raw_ace);
      PSID ace_sid = const_cast<DWORD*>(&ace->SidStart);
      const DWORD sid_length = IsValidSid(ace_sid) ? GetLengthSid(ace_sid) : 0;
      const DWORD expected_size =
          static_cast<DWORD>(offsetof(ACCESS_ALLOWED_ACE, SidStart)) +
          sid_length;
      if (ace->Header.AceType != ACCESS_ALLOWED_ACE_TYPE ||
          ace->Header.AceFlags != expected_flags ||
          (ace->Header.AceFlags & INHERITED_ACE) != 0 ||
          ace->Mask != kPrivateWindowsFullControl || sid_length == 0 ||
          ace->Header.AceSize != expected_size) {
        valid = false;
        break;
      }
      if (EqualSid(ace_sid, user_sid_) != 0 && !user_allowed) {
        user_allowed = true;
      } else if (EqualSid(ace_sid, system_sid_) != 0 && !system_allowed) {
        system_allowed = true;
      } else {
        valid = false;
      }
    }
    (void)LocalFree(descriptor);
    valid = valid && user_allowed && system_allowed;
    if (valid && identity) {
      *identity = observed;
    }
    return valid;
  }

  bool HardenHandle(HANDLE handle,
                    bool directory,
                    WindowsObjectIdentity* identity = nullptr) const noexcept {
    if (!ready_ || !OwnerIsCurrentUser(handle)) {
      return false;
    }
    PACL acl = directory ? directory_acl_ : file_acl_;
    const DWORD result = SetSecurityInfo(
        handle, SE_FILE_OBJECT,
        DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
        nullptr, nullptr, acl, nullptr);
    return result == ERROR_SUCCESS &&
           ValidateHandle(handle, directory, identity);
  }

 private:
  bool OwnerIsCurrentUser(HANDLE handle) const noexcept {
    PSID owner = nullptr;
    PSECURITY_DESCRIPTOR descriptor = nullptr;
    const DWORD result = GetSecurityInfo(
        handle, SE_FILE_OBJECT, OWNER_SECURITY_INFORMATION, &owner, nullptr,
        nullptr, nullptr, &descriptor);
    BOOL owner_defaulted = TRUE;
    PSID descriptor_owner = nullptr;
    const bool valid = result == ERROR_SUCCESS && descriptor &&
                       GetSecurityDescriptorOwner(descriptor,
                                                  &descriptor_owner,
                                                  &owner_defaulted) != 0 &&
                       descriptor_owner && !owner_defaulted &&
                       EqualSid(owner, user_sid_) != 0 &&
                       EqualSid(descriptor_owner, user_sid_) != 0;
    if (descriptor) {
      (void)LocalFree(descriptor);
    }
    return valid;
  }

  bool BuildAcl(bool directory, PACL* output) noexcept {
    std::array<EXPLICIT_ACCESSW, 2> access{};
    for (EXPLICIT_ACCESSW& entry : access) {
      entry.grfAccessPermissions = kPrivateWindowsFullControl;
      entry.grfAccessMode = SET_ACCESS;
      entry.grfInheritance = directory
                                 ? OBJECT_INHERIT_ACE |
                                       CONTAINER_INHERIT_ACE
                                 : NO_INHERITANCE;
    }
    BuildTrusteeWithSidW(&access[0].Trustee, user_sid_);
    BuildTrusteeWithSidW(&access[1].Trustee, system_sid_);
    return output &&
           SetEntriesInAclW(static_cast<ULONG>(access.size()), access.data(),
                            nullptr, output) == ERROR_SUCCESS;
  }

  bool InitializeDescriptor(bool directory,
                            SECURITY_DESCRIPTOR* descriptor) noexcept {
    PACL acl = directory ? directory_acl_ : file_acl_;
    return InitializeSecurityDescriptor(descriptor,
                                        SECURITY_DESCRIPTOR_REVISION) != 0 &&
           SetSecurityDescriptorOwner(descriptor, user_sid_, FALSE) != 0 &&
           SetSecurityDescriptorDacl(descriptor, TRUE, acl, FALSE) != 0 &&
           SetSecurityDescriptorControl(descriptor, SE_DACL_PROTECTED,
                                        SE_DACL_PROTECTED) != 0;
  }

  std::vector<unsigned char> sid_storage_;
  PSID user_sid_{nullptr};
  std::array<unsigned char, SECURITY_MAX_SID_SIZE> system_sid_storage_{};
  PSID system_sid_{nullptr};
  PACL file_acl_{nullptr};
  PACL directory_acl_{nullptr};
  SECURITY_DESCRIPTOR file_descriptor_{};
  SECURITY_DESCRIPTOR directory_descriptor_{};
  SECURITY_ATTRIBUTES file_attributes_{};
  SECURITY_ATTRIBUTES directory_attributes_{};
  bool ready_{false};
};

bool ValidatePrivateWindowsPath(
    const fs::path& path,
    bool directory,
    WindowsObjectIdentity* identity = nullptr) noexcept {
  try {
    std::vector<WindowsPathComponent> before;
    if (!InspectWindowsPathChain(path, directory, &before)) {
      return false;
    }
    HANDLE handle = OpenWindowsPathNoReparse(
        path, FILE_READ_ATTRIBUTES | READ_CONTROL);
    PrivateWindowsSecurity security;
    WindowsObjectIdentity opened;
    const bool opened_valid =
        handle != INVALID_HANDLE_VALUE && security.ready() &&
        security.ValidateHandle(handle, directory, &opened) &&
        opened == before.back().identity;
    if (handle != INVALID_HANDLE_VALUE) {
      (void)CloseHandle(handle);
    }
    std::vector<WindowsPathComponent> after;
    const bool valid = opened_valid &&
                       InspectWindowsPathChain(path, directory, &after) &&
                       SameWindowsPathChain(before, after) &&
                       opened == after.back().identity;
    if (valid && identity) {
      *identity = opened;
    }
    return valid;
  } catch (...) {
    return false;
  }
}

bool HardenAndValidateOpenedWindowsPath(
    const fs::path& path,
    HANDLE handle,
    bool directory,
    const PrivateWindowsSecurity& security,
    WindowsObjectIdentity* identity = nullptr) noexcept {
  try {
    std::vector<WindowsPathComponent> before;
    WindowsObjectIdentity opened;
    if (!InspectWindowsPathChain(path, directory, &before) ||
        !WindowsIdentityForHandle(handle, &opened) ||
        !(opened == before.back().identity) ||
        !security.HardenHandle(handle, directory, &opened)) {
      return false;
    }
    std::vector<WindowsPathComponent> after;
    const bool valid = InspectWindowsPathChain(path, directory, &after) &&
                       SameWindowsPathChain(before, after) &&
                       opened == after.back().identity;
    if (valid && identity) {
      *identity = opened;
    }
    return valid;
  } catch (...) {
    return false;
  }
}

bool HardenPrivateWindowsPath(const fs::path& path,
                              bool directory) noexcept {
  PrivateWindowsSecurity security;
  if (!security.ready()) {
    return false;
  }
  HANDLE handle = OpenWindowsPathNoReparse(
      path, FILE_READ_ATTRIBUTES | READ_CONTROL | WRITE_DAC);
  const bool valid = handle != INVALID_HANDLE_VALUE &&
                     HardenAndValidateOpenedWindowsPath(path, handle,
                                                        directory, security);
  if (handle != INVALID_HANDLE_VALUE) {
    (void)CloseHandle(handle);
  }
  return valid && ValidatePrivateWindowsPath(path, directory);
}

bool EnsurePrivateDirectory(const fs::path& input) noexcept {
  try {
    fs::path path;
    std::vector<fs::path> components;
    if (!DecomposeWindowsPath(input, &path, &components) ||
        path == path.root_path() || !HasExpectedSpoolSuffix(path)) {
      return false;
    }
    PrivateWindowsSecurity security;
    if (!security.ready()) {
      return false;
    }
    for (std::size_t index = 1; index < components.size(); ++index) {
      const bool managed = index + 4 >= components.size();
      if (!CreateDirectoryW(components[index].c_str(),
                            managed ? security.attributes(true) : nullptr) &&
          GetLastError() != ERROR_ALREADY_EXISTS) {
        return false;
      }
      std::vector<WindowsPathComponent> observed;
      if (!InspectWindowsPathChain(components[index], true, &observed)) {
        return false;
      }
    }
    std::vector<WindowsPathComponent> before;
    if (!InspectWindowsPathChain(path, true, &before)) {
      return false;
    }
    fs::path managed = path;
    for (int level = 0; level < 4; ++level) {
      if (managed == managed.root_path() ||
          !HardenPrivateWindowsPath(managed, true)) {
        return false;
      }
      managed = managed.parent_path();
    }
    std::vector<WindowsPathComponent> after;
    return InspectWindowsPathChain(path, true, &after) &&
           SameWindowsPathChain(before, after) &&
           ValidatePrivateWindowsPath(path, true);
  } catch (...) {
    return false;
  }
}

std::optional<fs::path> DefaultWindowsSpoolDirectory() noexcept {
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
    const fs::path incoming =
        fs::path(local_app_data) / L"YunPinIME" / L"sync" /
        L"native-events" / L"incoming";
    CoTaskMemFree(local_app_data);
    local_app_data = nullptr;
    fs::path normalized;
    std::vector<fs::path> components;
    if (!DecomposeWindowsPath(incoming, &normalized, &components) ||
        !HasExpectedSpoolSuffix(normalized)) {
      return std::nullopt;
    }
    return normalized;
  } catch (...) {
    if (local_app_data) {
      CoTaskMemFree(local_app_data);
    }
    return std::nullopt;
  }
}

bool WriteAll(HANDLE file, const std::string& data) noexcept {
  std::size_t offset = 0;
  while (offset < data.size()) {
    DWORD written = 0;
    if (!WriteFile(file, data.data() + offset,
                   static_cast<DWORD>(data.size() - offset), &written,
                   nullptr) || written == 0) {
      return false;
    }
    offset += written;
  }
  return true;
}

class ScopedSpoolLock {
 public:
  ~ScopedSpoolLock() {
    if (file_ != INVALID_HANDLE_VALUE) {
      if (locked_) {
        (void)UnlockFileEx(file_, 0, MAXDWORD, MAXDWORD, &overlapped_);
      }
      (void)CloseHandle(file_);
    }
  }

  bool TryAcquire(const fs::path& directory) noexcept {
    try {
      const fs::path path = directory / L".spool.lock";
      PrivateWindowsSecurity security;
      if (!security.ready()) {
        return false;
      }
      file_ = CreateFileW(
          path.c_str(), GENERIC_READ | GENERIC_WRITE |
                            FILE_READ_ATTRIBUTES | READ_CONTROL |
                            WRITE_DAC | WRITE_OWNER,
          FILE_SHARE_READ | FILE_SHARE_WRITE, security.attributes(false),
          OPEN_ALWAYS,
          FILE_ATTRIBUTE_NORMAL | FILE_FLAG_OPEN_REPARSE_POINT, nullptr);
      LARGE_INTEGER size{};
      if (file_ == INVALID_HANDLE_VALUE ||
          !HardenAndValidateOpenedWindowsPath(path, file_, false, security) ||
          !ValidatePrivateWindowsPath(path, false) ||
          !GetFileSizeEx(file_, &size) || size.QuadPart != 0) {
        return false;
      }
      locked_ = LockFileEx(file_, LOCKFILE_EXCLUSIVE_LOCK |
                                      LOCKFILE_FAIL_IMMEDIATELY,
                           0, MAXDWORD, MAXDWORD, &overlapped_) != 0;
      return locked_;
    } catch (...) {
      return false;
    }
  }

 private:
  HANDLE file_{INVALID_HANDLE_VALUE};
  OVERLAPPED overlapped_{};
  bool locked_{false};
};

std::optional<std::string> ReadPrivateBoundedFile(
    const fs::path& path) noexcept {
  try {
    std::vector<WindowsPathComponent> before;
    if (!InspectWindowsPathChain(path, false, &before)) {
      return std::nullopt;
    }
    HANDLE file = OpenWindowsPathNoReparse(
        path, GENERIC_READ | FILE_READ_ATTRIBUTES | READ_CONTROL,
        FILE_SHARE_READ);
    PrivateWindowsSecurity security;
    WindowsObjectIdentity identity;
    if (file == INVALID_HANDLE_VALUE || !security.ready() ||
        !security.ValidateHandle(file, false, &identity) ||
        !(identity == before.back().identity)) {
      if (file != INVALID_HANDLE_VALUE) {
        (void)CloseHandle(file);
      }
      return std::nullopt;
    }
    LARGE_INTEGER size{};
    if (!GetFileSizeEx(file, &size) || size.QuadPart < 1 ||
        size.QuadPart > static_cast<LONGLONG>(kMaxSerializedEventBytes)) {
      (void)CloseHandle(file);
      return std::nullopt;
    }
    std::string contents(static_cast<std::size_t>(size.QuadPart), '\0');
    std::size_t offset = 0;
    while (offset < contents.size()) {
      DWORD read = 0;
      if (!ReadFile(file, contents.data() + offset,
                    static_cast<DWORD>(contents.size() - offset), &read,
                    nullptr) || read == 0) {
        (void)CloseHandle(file);
        return std::nullopt;
      }
      offset += read;
    }
    char trailing = '\0';
    DWORD trailing_read = 0;
    const bool exact = ReadFile(file, &trailing, 1, &trailing_read, nullptr) &&
                       trailing_read == 0;
    std::vector<WindowsPathComponent> after;
    const bool bound = InspectWindowsPathChain(path, false, &after) &&
                       SameWindowsPathChain(before, after) &&
                       identity == after.back().identity;
    (void)CloseHandle(file);
    return exact && bound ? std::optional<std::string>(std::move(contents))
                          : std::nullopt;
  } catch (...) {
    return std::nullopt;
  }
}

std::optional<std::uintmax_t> PrivateWindowsFileSize(
    const fs::path& path) noexcept {
  try {
    std::vector<WindowsPathComponent> before;
    if (!InspectWindowsPathChain(path, false, &before)) {
      return std::nullopt;
    }
    HANDLE file = OpenWindowsPathNoReparse(
        path, FILE_READ_ATTRIBUTES | READ_CONTROL);
    PrivateWindowsSecurity security;
    WindowsObjectIdentity identity;
    LARGE_INTEGER size{};
    const bool opened = file != INVALID_HANDLE_VALUE && security.ready() &&
                        security.ValidateHandle(file, false, &identity) &&
                        WindowsHandleMatchesPath(file, path) &&
                        identity == before.back().identity &&
                        GetFileSizeEx(file, &size) != 0 &&
                        size.QuadPart >= 0;
    std::vector<WindowsPathComponent> after;
    const bool bound = opened &&
                       InspectWindowsPathChain(path, false, &after) &&
                       SameWindowsPathChain(before, after) &&
                       identity == after.back().identity;
    if (file != INVALID_HANDLE_VALUE) {
      (void)CloseHandle(file);
    }
    return bound
               ? std::optional<std::uintmax_t>(
                     static_cast<std::uintmax_t>(size.QuadPart))
               : std::nullopt;
  } catch (...) {
    return std::nullopt;
  }
}

bool InstallRecoveredTemp(const fs::path& temp_path,
                          const fs::path& final_path,
                          const NativeSelectionEvent& event) noexcept {
  WindowsObjectIdentity source_identity;
  if (!ValidatePrivateWindowsPath(temp_path, false, &source_identity)) {
    return false;
  }
  const DWORD attributes = GetFileAttributesW(final_path.c_str());
  if (attributes != INVALID_FILE_ATTRIBUTES) {
    if (!ValidatePrivateWindowsPath(final_path, false) ||
        !FileContainsExpectedEvent(final_path, event)) {
      return false;
    }
    return DeleteFileW(temp_path.c_str()) != 0;
  }
  const DWORD attributes_error = GetLastError();
  if (attributes_error != ERROR_FILE_NOT_FOUND &&
      attributes_error != ERROR_PATH_NOT_FOUND) {
    return false;
  }
  if (MoveFileExW(temp_path.c_str(), final_path.c_str(),
                  MOVEFILE_WRITE_THROUGH)) {
    WindowsObjectIdentity final_identity;
    return ValidatePrivateWindowsPath(final_path, false, &final_identity) &&
           final_identity == source_identity &&
           FileContainsExpectedEvent(final_path, event);
  }
  const DWORD move_error = GetLastError();
  if ((move_error == ERROR_FILE_EXISTS ||
       move_error == ERROR_ALREADY_EXISTS) &&
      ValidatePrivateWindowsPath(final_path, false) &&
      FileContainsExpectedEvent(final_path, event)) {
    return DeleteFileW(temp_path.c_str()) != 0;
  }
  return false;
}

#else

bool HasExpectedSpoolSuffix(const fs::path& path) {
  return path.filename() == "incoming" &&
         path.parent_path().filename() == "native-events" &&
         path.parent_path().parent_path().filename() == "Sync" &&
         path.parent_path().parent_path().parent_path().filename() == "YunPin";
}

bool IsDirectoryWithoutSymlink(const fs::path& path) noexcept {
  struct stat status {};
  return ::lstat(path.c_str(), &status) == 0 && S_ISDIR(status.st_mode);
}

bool IsSecureRegularFile(const fs::path& path) noexcept {
  struct stat status {};
  return ::lstat(path.c_str(), &status) == 0 && S_ISREG(status.st_mode) &&
         status.st_uid == ::geteuid() && (status.st_mode & 0777) == 0600;
}

bool HardenOwnedDirectory(const fs::path& path) noexcept {
  struct stat status {};
  if (::lstat(path.c_str(), &status) != 0 || !S_ISDIR(status.st_mode) ||
      status.st_uid != ::geteuid() || ::chmod(path.c_str(), 0700) != 0 ||
      ::lstat(path.c_str(), &status) != 0) {
    return false;
  }
  return S_ISDIR(status.st_mode) && status.st_uid == ::geteuid() &&
         (status.st_mode & 0777) == 0700;
}

bool EnsurePrivateDirectory(const fs::path& input) noexcept {
  try {
    const fs::path path = input.lexically_normal();
    if (!path.is_absolute() || path == path.root_path() ||
        !HasExpectedSpoolSuffix(path)) {
      return false;
    }
    fs::path current = path.root_path();
    for (const fs::path& component : path.relative_path()) {
      if (component.empty() || component == fs::path(".") ||
          component == fs::path("..")) {
        return false;
      }
      current /= component;
      if (::mkdir(current.c_str(), 0700) == 0) {
        if (::chmod(current.c_str(), 0700) != 0) {
          return false;
        }
      } else if (errno != EEXIST) {
        return false;
      }
      if (!IsDirectoryWithoutSymlink(current)) {
        return false;
      }
    }
    fs::path managed = path;
    for (int level = 0; level < 4; ++level) {
      if (managed == managed.root_path() || !HardenOwnedDirectory(managed)) {
        return false;
      }
      managed = managed.parent_path();
    }
    return true;
  } catch (...) {
    return false;
  }
}

bool WriteAll(int file, const std::string& data) noexcept {
  std::size_t offset = 0;
  while (offset < data.size()) {
    const ssize_t written =
        ::write(file, data.data() + offset, data.size() - offset);
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

void SyncDirectory(const fs::path& directory) noexcept {
#if defined(O_DIRECTORY)
  const int descriptor =
      ::open(directory.c_str(), O_RDONLY | O_DIRECTORY | O_CLOEXEC);
#else
  const int descriptor = ::open(directory.c_str(), O_RDONLY | O_CLOEXEC);
#endif
  if (descriptor >= 0) {
    (void)::fsync(descriptor);
    (void)::close(descriptor);
  }
}

class ScopedSpoolLock {
 public:
  ScopedSpoolLock() = default;
  ~ScopedSpoolLock() {
    if (file_ >= 0) {
      (void)::flock(file_, LOCK_UN);
      (void)::close(file_);
    }
  }

  bool TryAcquire(const fs::path& directory) noexcept {
    try {
      const fs::path path = directory / ".spool.lock";
      int flags = O_RDWR | O_CREAT | O_CLOEXEC;
#if defined(O_NOFOLLOW)
      flags |= O_NOFOLLOW;
#endif
      file_ = ::open(path.c_str(), flags, 0600);
      if (file_ < 0) {
        return false;
      }
      struct stat opened {};
      struct stat named {};
      if (::fchmod(file_, 0600) != 0 || ::fstat(file_, &opened) != 0 ||
          ::lstat(path.c_str(), &named) != 0 ||
          !S_ISREG(opened.st_mode) || !S_ISREG(named.st_mode) ||
          opened.st_uid != ::geteuid() || named.st_uid != ::geteuid() ||
          (opened.st_mode & 0777) != 0600 ||
          (named.st_mode & 0777) != 0600 || opened.st_size != 0 ||
          named.st_size != 0 || opened.st_dev != named.st_dev ||
          opened.st_ino != named.st_ino || opened.st_nlink != 1) {
        return false;
      }
      return ::flock(file_, LOCK_EX | LOCK_NB) == 0;
    } catch (...) {
      return false;
    }
  }

 private:
  int file_{-1};
};

std::optional<std::string> ReadPrivateBoundedFile(
    const fs::path& path) noexcept {
  try {
    int flags = O_RDONLY | O_CLOEXEC;
#if defined(O_NOFOLLOW)
    flags |= O_NOFOLLOW;
#endif
    const int file = ::open(path.c_str(), flags);
    if (file < 0) {
      return std::nullopt;
    }
    struct stat status {};
    struct stat named {};
    if (::fstat(file, &status) != 0 ||
        ::lstat(path.c_str(), &named) != 0 || !S_ISREG(status.st_mode) ||
        !S_ISREG(named.st_mode) ||
        status.st_uid != ::geteuid() || (status.st_mode & 0777) != 0600 ||
        named.st_uid != ::geteuid() || (named.st_mode & 0777) != 0600 ||
        status.st_nlink != 1 || named.st_nlink != 1 ||
        status.st_dev != named.st_dev || status.st_ino != named.st_ino ||
        status.st_size != named.st_size || status.st_size < 1 ||
        status.st_size > static_cast<off_t>(kMaxSerializedEventBytes)) {
      (void)::close(file);
      return std::nullopt;
    }
    std::string contents(static_cast<std::size_t>(status.st_size), '\0');
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
    char trailing = '\0';
    const ssize_t extra = ::read(file, &trailing, 1);
    (void)::close(file);
    return extra == 0 ? std::optional<std::string>(std::move(contents))
                      : std::nullopt;
  } catch (...) {
    return std::nullopt;
  }
}

bool InstallRecoveredTemp(const fs::path& temp_path,
                          const fs::path& final_path,
                          const NativeSelectionEvent& event) noexcept {
  struct stat existing {};
  if (::lstat(final_path.c_str(), &existing) == 0) {
    if (!IsSecureRegularFile(final_path) ||
        !FileContainsExpectedEvent(final_path, event)) {
      return false;
    }
    const bool removed = ::unlink(temp_path.c_str()) == 0;
    if (removed) {
      SyncDirectory(final_path.parent_path());
    }
    return removed;
  }
  if (errno != ENOENT) {
    return false;
  }

  bool installed = false;
#if defined(__APPLE__)
  installed = ::renamex_np(temp_path.c_str(), final_path.c_str(),
                           RENAME_EXCL) == 0;
  const int install_error = installed ? 0 : errno;
#else
  installed = ::link(temp_path.c_str(), final_path.c_str()) == 0;
  const int install_error = installed ? 0 : errno;
  if (installed) {
    (void)::unlink(temp_path.c_str());
  }
#endif
  if (installed) {
    SyncDirectory(final_path.parent_path());
    return IsSecureRegularFile(final_path) &&
           FileContainsExpectedEvent(final_path, event);
  }
  if (install_error == EEXIST && IsSecureRegularFile(final_path) &&
      FileContainsExpectedEvent(final_path, event)) {
    const bool removed = ::unlink(temp_path.c_str()) == 0;
    if (removed) {
      SyncDirectory(final_path.parent_path());
    }
    return removed;
  }
  return false;
}

#endif

enum class SpoolQuotaResult { kAllowed, kFull, kUnsafe };
enum class SpoolWriteResult { kSuccess, kRetry, kDrop };

bool IsSpoolLockPath(const fs::path& path) noexcept {
#if defined(_WIN32)
  return path.filename() == L".spool.lock";
#else
  return path.filename() == ".spool.lock";
#endif
}

bool RecoverStagedEvents(const fs::path& directory) noexcept {
  try {
    for (const auto& entry : fs::directory_iterator(directory)) {
      const fs::path& temp_path = entry.path();
      const std::string filename = temp_path.filename().u8string();
      if (filename.rfind(".tmp-", 0) != 0) {
        continue;
      }
      const auto contents = ReadPrivateBoundedFile(temp_path);
      if (!contents) {
        return false;
      }
      const auto event = ParseSerializedEvent(*contents);
      if (!event || !IsExpectedTempName(filename, event->event_id)) {
        return false;
      }
      const fs::path final_path = directory / (event->event_id + ".json");
      if (!InstallRecoveredTemp(temp_path, final_path, *event)) {
        return false;
      }
    }
    return true;
  } catch (...) {
    return false;
  }
}

bool RecoverSpoolOnStart(const fs::path& directory) noexcept {
  ScopedSpoolLock spool_lock;
  return spool_lock.TryAcquire(directory) && RecoverStagedEvents(directory);
}

SpoolQuotaResult CheckSpoolQuota(const fs::path& directory,
                                 std::size_t pending_bytes) noexcept {
  try {
    std::size_t entries = 0;
    std::uintmax_t total_bytes = 0;
    for (const auto& entry : fs::directory_iterator(directory)) {
      const fs::path& path = entry.path();
      if (IsSpoolLockPath(path)) {
#if defined(_WIN32)
        const auto lock_bytes = PrivateWindowsFileSize(path);
        if (!lock_bytes || *lock_bytes != 0) {
          return SpoolQuotaResult::kUnsafe;
        }
#else
        struct stat lock_status {};
        if (::lstat(path.c_str(), &lock_status) != 0 ||
            !S_ISREG(lock_status.st_mode) ||
            lock_status.st_uid != ::geteuid() ||
            (lock_status.st_mode & 0777) != 0600 ||
            lock_status.st_size != 0 || lock_status.st_nlink != 1) {
          return SpoolQuotaResult::kUnsafe;
        }
#endif
        continue;
      }
      if (++entries > kMaxSpoolEntries) {
        return SpoolQuotaResult::kFull;
      }
#if defined(_WIN32)
      const auto observed_bytes = PrivateWindowsFileSize(path);
      if (!observed_bytes) {
        return SpoolQuotaResult::kUnsafe;
      }
#else
      if (!IsSecureRegularFile(path)) {
        return SpoolQuotaResult::kUnsafe;
      }
#endif
#if defined(_WIN32)
      const std::uintmax_t file_bytes = *observed_bytes;
#else
      const std::uintmax_t file_bytes = fs::file_size(path);
#endif
      if (file_bytes > kMaxSpoolBytes - total_bytes) {
        return SpoolQuotaResult::kFull;
      }
      total_bytes += file_bytes;
    }
    if (entries >= kMaxSpoolEntries || pending_bytes > kMaxSpoolBytes ||
        total_bytes > kMaxSpoolBytes - pending_bytes) {
      return SpoolQuotaResult::kFull;
    }
    return SpoolQuotaResult::kAllowed;
  } catch (...) {
    return SpoolQuotaResult::kUnsafe;
  }
}

std::atomic<std::uint64_t> next_temp_sequence{1};

SpoolWriteResult WriteEventFile(const fs::path& directory,
                                const NativeSelectionEvent& event) noexcept {
  const std::optional<std::string> serialized = SerializeEvent(event);
  if (!serialized) {
    return SpoolWriteResult::kDrop;
  }
  if (!EnsurePrivateDirectory(directory)) {
    return SpoolWriteResult::kRetry;
  }
  try {
    // All frontend processes sharing this per-user spool cooperate on one
    // persistent lock.  The lock is acquired only on this background thread;
    // input callbacks still perform no filesystem operation and never wait.
    ScopedSpoolLock spool_lock;
    if (!spool_lock.TryAcquire(directory) ||
        !RecoverStagedEvents(directory)) {
      return SpoolWriteResult::kRetry;
    }
    const fs::path final_path = directory / (event.event_id + ".json");
#if defined(_WIN32)
    const DWORD final_attributes = GetFileAttributesW(final_path.c_str());
    if (final_attributes != INVALID_FILE_ATTRIBUTES) {
      return ValidatePrivateWindowsPath(final_path, false) &&
                     FileContainsExpectedEvent(final_path, event)
                 ? SpoolWriteResult::kSuccess
                 : SpoolWriteResult::kDrop;
    }
    const DWORD final_attributes_error = GetLastError();
    if (final_attributes_error != ERROR_FILE_NOT_FOUND &&
        final_attributes_error != ERROR_PATH_NOT_FOUND) {
      return SpoolWriteResult::kRetry;
    }
#else
    struct stat existing {};
    if (::lstat(final_path.c_str(), &existing) == 0) {
      return IsSecureRegularFile(final_path) &&
                     FileContainsExpectedEvent(final_path, event)
                 ? SpoolWriteResult::kSuccess
                 : SpoolWriteResult::kDrop;
    }
    if (errno != ENOENT) {
      return SpoolWriteResult::kRetry;
    }
#endif

    const SpoolQuotaResult quota =
        CheckSpoolQuota(directory, serialized->size());
    if (quota == SpoolQuotaResult::kFull) {
      return SpoolWriteResult::kDrop;
    }
    if (quota != SpoolQuotaResult::kAllowed) {
      return SpoolWriteResult::kRetry;
    }

    for (unsigned int attempt = 0; attempt < 8; ++attempt) {
      std::array<char, 17> temp_sequence{};
      const auto converted = std::to_chars(
          temp_sequence.data(), temp_sequence.data() + temp_sequence.size() - 1,
          next_temp_sequence.fetch_add(1, std::memory_order_relaxed), 16);
      if (converted.ec != std::errc()) {
        return SpoolWriteResult::kDrop;
      }
      std::string temp_name = ".tmp-" + event.event_id + '-';
      temp_name.append(temp_sequence.data(), converted.ptr);
      const fs::path temp_path = directory / temp_name;

#if defined(_WIN32)
      PrivateWindowsSecurity security;
      if (!security.ready()) {
        return SpoolWriteResult::kRetry;
      }
      HANDLE file = CreateFileW(
          temp_path.c_str(),
          GENERIC_WRITE | FILE_READ_ATTRIBUTES | READ_CONTROL | WRITE_DAC,
          FILE_SHARE_READ | FILE_SHARE_WRITE, security.attributes(false),
          CREATE_NEW,
          FILE_ATTRIBUTE_NORMAL | FILE_FLAG_OPEN_REPARSE_POINT, nullptr);
      if (file == INVALID_HANDLE_VALUE) {
        if (GetLastError() == ERROR_FILE_EXISTS ||
            GetLastError() == ERROR_ALREADY_EXISTS) {
          continue;
        }
        return SpoolWriteResult::kRetry;
      }
      WindowsObjectIdentity created_identity;
      const bool durable = WriteAll(file, *serialized) && FlushFileBuffers(file);
      const bool hardened = durable &&
          HardenAndValidateOpenedWindowsPath(temp_path, file, false, security,
                                               &created_identity);
      const bool closed = CloseHandle(file) != 0;
      WindowsObjectIdentity reopened_identity;
      if (!durable || !hardened || !closed ||
          !ValidatePrivateWindowsPath(temp_path, false, &reopened_identity) ||
          !(reopened_identity == created_identity)) {
        (void)DeleteFileW(temp_path.c_str());
        return SpoolWriteResult::kRetry;
      }
      if (MoveFileExW(temp_path.c_str(), final_path.c_str(),
                      MOVEFILE_WRITE_THROUGH)) {
        WindowsObjectIdentity final_identity;
        return ValidatePrivateWindowsPath(final_path, false, &final_identity) &&
                       final_identity == created_identity &&
                       FileContainsExpectedEvent(final_path, event)
                   ? SpoolWriteResult::kSuccess : SpoolWriteResult::kDrop;
      }
      const DWORD move_error = GetLastError();
      (void)DeleteFileW(temp_path.c_str());
      if ((move_error == ERROR_FILE_EXISTS ||
           move_error == ERROR_ALREADY_EXISTS)) {
        return ValidatePrivateWindowsPath(final_path, false) &&
                       FileContainsExpectedEvent(final_path, event)
                   ? SpoolWriteResult::kSuccess : SpoolWriteResult::kDrop;
      }
      return SpoolWriteResult::kRetry;
#else
      int flags = O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC;
#if defined(O_NOFOLLOW)
      flags |= O_NOFOLLOW;
#endif
      const int file = ::open(temp_path.c_str(), flags, 0600);
      if (file < 0) {
        if (errno == EEXIST) {
          continue;
        }
        return SpoolWriteResult::kRetry;
      }
      const bool durable = ::fchmod(file, 0600) == 0 &&
                           WriteAll(file, *serialized) && ::fsync(file) == 0;
      const bool closed = ::close(file) == 0;
      if (!durable || !closed) {
        (void)::unlink(temp_path.c_str());
        return SpoolWriteResult::kRetry;
      }

      bool installed = false;
#if defined(__APPLE__)
      installed = ::renamex_np(temp_path.c_str(), final_path.c_str(),
                               RENAME_EXCL) == 0;
      const int install_error = installed ? 0 : errno;
#else
      installed = ::link(temp_path.c_str(), final_path.c_str()) == 0;
      const int install_error = installed ? 0 : errno;
      if (installed) {
        (void)::unlink(temp_path.c_str());
      }
#endif
      if (installed) {
        SyncDirectory(directory);
        return IsSecureRegularFile(final_path)
                       && FileContainsExpectedEvent(final_path, event)
                   ? SpoolWriteResult::kSuccess : SpoolWriteResult::kDrop;
      }
      (void)::unlink(temp_path.c_str());
      if (install_error == EEXIST &&
          IsSecureRegularFile(final_path) &&
          FileContainsExpectedEvent(final_path, event)) {
        return SpoolWriteResult::kSuccess;
      }
      return SpoolWriteResult::kRetry;
#endif
    }
  } catch (...) {
    return SpoolWriteResult::kRetry;
  }
  return SpoolWriteResult::kRetry;
}

}  // namespace

class NativeSelectionEventQueue::Impl {
 public:
  Impl() : process_prefix_(RandomProcessPrefix()) {}

  bool TryPublish(std::string_view phrase,
                  std::string_view normalized_pinyin) noexcept {
    if (!IsSafeUtf8(phrase) || !IsAsciiPinyin(normalized_pinyin)) {
      dropped_.fetch_add(1, std::memory_order_relaxed);
      return false;
    }
    try {
      const std::uint64_t sequence =
          next_sequence_.fetch_add(1, std::memory_order_relaxed);
      std::array<char, 17> sequence_text{};
      const auto converted = std::to_chars(
          sequence_text.data(), sequence_text.data() + sequence_text.size() - 1,
          sequence, 16);
      if (converted.ec != std::errc()) {
        dropped_.fetch_add(1, std::memory_order_relaxed);
        return false;
      }
      std::string identifier = process_prefix_;
      identifier.push_back('-');
      identifier.append(sequence_text.data(), converted.ptr);
      NativeSelectionEvent event{std::move(identifier), std::string(phrase),
                                 std::string(normalized_pinyin)};
      if (event.event_id.size() > kMaxEventIdBytes) {
        dropped_.fetch_add(1, std::memory_order_relaxed);
        return false;
      }

      std::unique_lock<std::mutex> lock(mutex_, std::try_to_lock);
      if (!lock.owns_lock() || !accepting_ || size_ == kCapacity) {
        dropped_.fetch_add(1, std::memory_order_relaxed);
        return false;
      }
      entries_[tail_] = std::move(event);
      tail_ = (tail_ + 1) % kCapacity;
      ++size_;
      lock.unlock();
      NotifyNativeSelectionSpooler();
      return true;
    } catch (...) {
      dropped_.fetch_add(1, std::memory_order_relaxed);
      return false;
    }
  }

  bool TryPop(NativeSelectionEvent* event) noexcept {
    if (!event) {
      return false;
    }
    try {
      std::unique_lock<std::mutex> lock(mutex_, std::try_to_lock);
      if (!lock.owns_lock() || size_ == 0) {
        return false;
      }
      *event = std::move(entries_[head_]);
      entries_[head_] = {};
      head_ = (head_ + 1) % kCapacity;
      --size_;
      return true;
    } catch (...) {
      return false;
    }
  }

  std::uint64_t dropped() const noexcept {
    return dropped_.load(std::memory_order_relaxed);
  }

  std::size_t DiscardAll() noexcept {
    try {
      std::lock_guard<std::mutex> lock(mutex_);
      const std::size_t discarded = size_;
      for (auto& entry : entries_) {
        entry = {};
      }
      head_ = 0;
      tail_ = 0;
      size_ = 0;
      return discarded;
    } catch (...) {
      return 0;
    }
  }

  void PausePublishingForSpoolerStop() noexcept {
    try {
      std::lock_guard<std::mutex> lock(mutex_);
      accepting_ = false;
    } catch (...) {
      // A valid std::mutex should not fail. If the runtime nevertheless
      // rejects the lock, TryPublish remains best-effort and Stop still uses
      // bounded queue accounting below.
    }
  }

  void ResumePublishingForSpoolerStart() noexcept {
    try {
      std::lock_guard<std::mutex> lock(mutex_);
      accepting_ = true;
    } catch (...) {
    }
  }

  std::size_t size() const noexcept {
    try {
      std::unique_lock<std::mutex> lock(mutex_, std::try_to_lock);
      return lock.owns_lock() ? size_ : 0;
    } catch (...) {
      return 0;
    }
  }

 private:
  const std::string process_prefix_;
  std::atomic<std::uint64_t> next_sequence_{1};
  std::atomic<std::uint64_t> dropped_{0};
  mutable std::mutex mutex_;
  std::array<NativeSelectionEvent, kCapacity> entries_{};
  std::size_t head_{0};
  std::size_t tail_{0};
  std::size_t size_{0};
  bool accepting_{true};
};

NativeSelectionEventQueue::NativeSelectionEventQueue()
    : impl_(std::make_unique<Impl>()) {}

NativeSelectionEventQueue::~NativeSelectionEventQueue() = default;

NativeSelectionEventQueue& NativeSelectionEventQueue::Instance() {
  static NativeSelectionEventQueue queue;
  return queue;
}

bool NativeSelectionEventQueue::TryPublish(
    std::string_view phrase, std::string_view normalized_pinyin) noexcept {
  return impl_->TryPublish(phrase, normalized_pinyin);
}

bool NativeSelectionEventQueue::TryPop(NativeSelectionEvent* event) noexcept {
  return impl_->TryPop(event);
}

std::size_t NativeSelectionEventQueue::DiscardAll() noexcept {
  return impl_->DiscardAll();
}

void NativeSelectionEventQueue::PausePublishingForSpoolerStop() noexcept {
  impl_->PausePublishingForSpoolerStop();
}

void NativeSelectionEventQueue::ResumePublishingForSpoolerStart() noexcept {
  impl_->ResumePublishingForSpoolerStart();
}

std::uint64_t NativeSelectionEventQueue::dropped() const noexcept {
  return impl_->dropped();
}

std::size_t NativeSelectionEventQueue::size() const noexcept {
  return impl_->size();
}

namespace {

class NativeSelectionSpooler;
std::atomic<NativeSelectionSpooler*> active_spooler{nullptr};

class NativeSelectionSpooler {
 public:
  static NativeSelectionSpooler& Instance() {
    static NativeSelectionSpooler spooler;
    return spooler;
  }

  ~NativeSelectionSpooler() {
    Stop();
    active_spooler.store(nullptr, std::memory_order_release);
  }

  bool Start(std::string_view absolute_utf8_directory) noexcept {
    if (absolute_utf8_directory.empty()) {
      return false;
    }
    fs::path directory;
    try {
      const fs::path requested = fs::u8path(absolute_utf8_directory);
#if defined(_WIN32)
      std::vector<fs::path> components;
      if (!DecomposeWindowsPath(requested, &directory, &components)) {
        return false;
      }
#else
      directory = requested.lexically_normal();
#endif
      if (!directory.is_absolute() || directory == directory.root_path()) {
        return false;
      }
    } catch (...) {
      return false;
    }
    // Surface an unusable or unsafe spool to the host immediately.  The
    // worker repeats this validation before every write to close later races.
    if (!EnsurePrivateDirectory(directory)) {
      return false;
    }

    std::lock_guard<std::mutex> lock(control_mutex_);
    if (stopping_) {
      return false;
    }
    if (worker_.joinable()) {
      return directory == directory_ && !stop_requested_;
    }
    directory_ = std::move(directory);
    stop_requested_ = false;
    try {
      worker_ = std::thread(&NativeSelectionSpooler::Run, this, directory_);
    } catch (...) {
      directory_.clear();
      return false;
    }
    NativeSelectionEventQueue::Instance().ResumePublishingForSpoolerStart();
    active_spooler.store(this, std::memory_order_release);
    return true;
  }

  void Stop() noexcept {
    std::thread worker;
    {
      std::lock_guard<std::mutex> lock(control_mutex_);
      if (!worker_.joinable() || stopping_) {
        return;
      }
      NativeSelectionEventQueue::Instance().PausePublishingForSpoolerStop();
      stopping_ = true;
      stop_requested_ = true;
      worker = std::move(worker_);
    }
    wakeup_.notify_all();
    if (worker.joinable()) {
      worker.join();
    }
    {
      std::lock_guard<std::mutex> lock(control_mutex_);
      stop_requested_ = false;
      stopping_ = false;
      directory_.clear();
    }
  }

  void Notify() noexcept { wakeup_.notify_one(); }

  std::uint64_t dropped() const noexcept {
    return dropped_.load(std::memory_order_relaxed);
  }

 private:
  NativeSelectionSpooler() = default;
  NativeSelectionSpooler(const NativeSelectionSpooler&) = delete;
  NativeSelectionSpooler& operator=(const NativeSelectionSpooler&) = delete;

  bool StopRequested() noexcept {
    std::lock_guard<std::mutex> lock(control_mutex_);
    return stop_requested_;
  }

  void WaitForWork() noexcept {
    std::unique_lock<std::mutex> lock(control_mutex_);
    if (!stop_requested_) {
      wakeup_.wait_for(lock, std::chrono::milliseconds(250));
    }
  }

  void Run(fs::path directory) noexcept {
    std::optional<NativeSelectionEvent> pending;
    std::size_t stop_drain_attempts = 0;
    bool startup_recovered = false;
    while (true) {
      const bool stopping = StopRequested();
      if (stopping && stop_drain_attempts >= kMaxStopDrainEvents) {
        const std::size_t discarded =
            (pending ? 1 : 0) +
            NativeSelectionEventQueue::Instance().DiscardAll();
        dropped_.fetch_add(discarded, std::memory_order_relaxed);
        return;
      }
      if (!startup_recovered) {
        startup_recovered = RecoverSpoolOnStart(directory);
        if (!startup_recovered) {
          if (stopping) {
            const std::size_t discarded =
                NativeSelectionEventQueue::Instance().DiscardAll();
            dropped_.fetch_add(discarded, std::memory_order_relaxed);
            return;
          }
          WaitForWork();
          continue;
        }
      }
      if (!pending) {
        NativeSelectionEvent event;
        if (NativeSelectionEventQueue::Instance().TryPop(&event)) {
          pending = std::move(event);
        } else if (stopping) {
          const std::size_t discarded =
              NativeSelectionEventQueue::Instance().DiscardAll();
          dropped_.fetch_add(discarded, std::memory_order_relaxed);
          return;
        } else {
          WaitForWork();
          continue;
        }
      }

      const SpoolWriteResult result = WriteEventFile(directory, *pending);
      if (stopping) {
        ++stop_drain_attempts;
      }
      if (result == SpoolWriteResult::kSuccess) {
        pending.reset();
        continue;
      }
      if (result == SpoolWriteResult::kDrop) {
        dropped_.fetch_add(1, std::memory_order_relaxed);
        pending.reset();
        continue;
      }
      if (stopping) {
        dropped_.fetch_add(1, std::memory_order_relaxed);
        pending.reset();
        const std::size_t discarded =
            NativeSelectionEventQueue::Instance().DiscardAll();
        dropped_.fetch_add(discarded, std::memory_order_relaxed);
        return;
      }
      // Retain the already-popped event and retry.  This deliberately avoids
      // logging its text or reordering later selections during a disk error.
      WaitForWork();
    }
  }

  mutable std::mutex control_mutex_;
  std::condition_variable wakeup_;
  std::thread worker_;
  fs::path directory_;
  bool stop_requested_{false};
  bool stopping_{false};
  std::atomic<std::uint64_t> dropped_{0};
};

void NotifyNativeSelectionSpooler() noexcept {
  if (NativeSelectionSpooler* spooler =
          active_spooler.load(std::memory_order_acquire)) {
    spooler->Notify();
  }
}

}  // namespace

}  // namespace yunpin

extern "C" bool YunPinTryPopNativeSelectionEventV1(
    YunPinNativeSelectionEventV1* event) noexcept {
  if (!event) {
    return false;
  }
  try {
    yunpin::NativeSelectionEvent value;
    if (!yunpin::NativeSelectionEventQueue::Instance().TryPop(&value)) {
      return false;
    }
    *event = {};
    event->version = yunpin::NativeSelectionEvent::kVersion;
    std::memcpy(event->event_id, value.event_id.data(), value.event_id.size());
    std::memcpy(event->phrase, value.phrase.data(), value.phrase.size());
    std::memcpy(event->pinyin, value.pinyin.data(), value.pinyin.size());
    return true;
  } catch (...) {
    return false;
  }
}

extern "C" bool YunPinStartNativeSelectionSpoolerV1(
    const char* absolute_utf8_directory) noexcept {
  if (!absolute_utf8_directory) {
    return false;
  }
  try {
    // Construct the queue before the worker singleton so process shutdown
    // always joins the spooler before destroying its source queue.
    (void)yunpin::NativeSelectionEventQueue::Instance();
    return yunpin::NativeSelectionSpooler::Instance().Start(
        absolute_utf8_directory);
  } catch (...) {
    return false;
  }
}

extern "C" bool YunPinStartDefaultNativeSelectionSpoolerV1() noexcept {
#if defined(_WIN32)
  try {
    const auto directory = yunpin::DefaultWindowsSpoolDirectory();
    if (!directory) {
      return false;
    }
    const std::string directory_utf8 = directory->u8string();
    return YunPinStartNativeSelectionSpoolerV1(directory_utf8.c_str());
  } catch (...) {
    return false;
  }
#else
  return false;
#endif
}

extern "C" void YunPinStopNativeSelectionSpoolerV1() noexcept {
  try {
    // Preserve singleton destruction order even when Stop is the first API
    // the host calls: the spooler must be destroyed before its source queue.
    (void)yunpin::NativeSelectionEventQueue::Instance();
    yunpin::NativeSelectionSpooler::Instance().Stop();
  } catch (...) {
  }
}

extern "C" std::uint64_t
YunPinDroppedNativeSelectionEventCount() noexcept {
  try {
    return yunpin::NativeSelectionEventQueue::Instance().dropped();
  } catch (...) {
    return 0;
  }
}

extern "C" std::uint64_t
YunPinNativeSelectionSpoolDropCount() noexcept {
  try {
    (void)yunpin::NativeSelectionEventQueue::Instance();
    return yunpin::NativeSelectionSpooler::Instance().dropped();
  } catch (...) {
    return 0;
  }
}
