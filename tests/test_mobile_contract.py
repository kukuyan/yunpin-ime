#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import ipaddress
import json
from pathlib import Path
import re
import unittest
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[1]
MOBILE = ROOT / "mobile"
CONTRACT_PATH = MOBILE / "contracts" / "mobile-protocol-freeze-v1.json"
FREEZE_DOC_PATH = ROOT / "docs" / "MOBILE_PROTOCOL_FREEZE.md"

TEXT_SUFFIXES = {
    ".c",
    ".cc",
    ".cpp",
    ".gradle",
    ".go",
    ".h",
    ".hpp",
    ".java",
    ".json",
    ".kt",
    ".kts",
    ".m",
    ".md",
    ".mm",
    ".plist",
    ".pbxproj",
    ".properties",
    ".swift",
    ".toml",
    ".entitlements",
    ".xml",
    ".yaml",
    ".yml",
}

TEXT_FILENAMES = {"CMakeLists.txt", "Makefile"}

SKIP_PARTS = {
    ".build",
    ".gradle",
    ".idea",
    "DerivedData",
    "build",
    "xcuserdata",
}

PRIVATE_NETWORK_RE = re.compile(
    r"(?<![0-9])(?:10(?:\.[0-9]{1,3}){3}|192\.168(?:\.[0-9]{1,3}){2}|"
    r"172\.(?:1[6-9]|2[0-9]|3[01])(?:\.[0-9]{1,3}){2}|"
    r"100\.(?:6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])(?:\.[0-9]{1,3}){2})(?![0-9])"
)

DEPLOYMENT_ALIAS_RE = re.compile(r"(?i)(?<![a-z0-9])(?:r0w|nas)(?![a-z0-9])")

ABSOLUTE_USER_PATH_RE = re.compile(
    r"(?:/Users/[A-Za-z0-9._-]+/|/home/[A-Za-z0-9._-]+/|/Volumes/[^/\r\n]+/|"
    r"[A-Za-z]:\\Users\\[A-Za-z0-9._ -]+\\)"
)

ASSIGNED_SERVER_LITERAL_RE = re.compile(
    r"(?ix)\b(?:endpoint|base[_A-Z]?url|relay[_A-Z]?(?:url|host|origin)|"
    r"server[_A-Z]?(?:url|host|origin|address))\b"
    r"\s*(?::[^=\n]+)?=\s*[\"']([^\"']+)[\"']"
)

SECRET_PATTERNS = {
    "private-key block": re.compile(r"-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----"),
    "YunPin recovery text": re.compile(r"(?i)\byprec1[0-9a-z]{16,}\b"),
    "GitHub token": re.compile(r"\b(?:ghp_|github_pat_)[A-Za-z0-9_]{20,}\b"),
    "AWS access key": re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    "literal bearer": re.compile(r"(?i)\bBearer\s+[A-Za-z0-9_-]{20,}\b"),
}

ASSIGNED_SECRET_RE = re.compile(
    r"(?ix)\b(?:password|passphrase|device[_A-Z]?token|pairing[_A-Z]?secret|"
    r"recovery[_A-Z]?(?:key|root|authentication)|api[_A-Z]?key)\b"
    r"\s*(?:=|:)\s*[\"']([^\"']{12,})[\"']"
)

DEVICE_CAP_RE = re.compile(
    r"(?ix)\b(?:max(?:imum)?(?:[_A-Z]*active)?[_A-Z]*devices?|"
    r"max[_A-Z]*device[_A-Z]*(?:count|limit|capacity)|"
    r"device[_A-Z]*(?:count|limit|capacity)|allowed[_A-Z]*devices?)\b"
    r"\s*(?::[^=\n]+)?=\s*2\b"
)

RECOVERY_ENTRYPOINT_PATTERNS = {
    "recovery HTTP route": re.compile(r"(?i)/v1/accounts/[^\s\"']*/recover\b"),
    "account recovery call": re.compile(r"(?i)\brecoverAccount\b"),
    "recovery-key API": re.compile(
        r"(?i)\b(?:generate|create|encode|decode|open|import|export|display|show)"
        r"[A-Za-z0-9_]*(?:RecoveryKey|RecoveryRoot|RecoveryAuthentication)\b"
    ),
    "silent account reset": re.compile(r"(?i)\b(?:reset|replace)Account(?:AndData)?\b"),
}

FORBIDDEN_DATA_NAMES = {
    "private.tsv",
    "credentials.json",
    "credential.json",
    "pairing-invitation.json",
    "recovery-key.txt",
}

FORBIDDEN_DATA_SUFFIXES = {
    ".db",
    ".har",
    ".jsonl",
    ".pcap",
    ".scel",
    ".sgpybin",
    ".sqlite",
    ".userdb",
}


def iter_mobile_files() -> list[Path]:
    return sorted(
        path
        for path in MOBILE.rglob("*")
        if path.is_file() and not any(part in SKIP_PARTS for part in path.parts)
    )


def iter_mobile_text_files() -> list[Path]:
    return [
        path
        for path in iter_mobile_files()
        if path.suffix.lower() in TEXT_SUFFIXES or path.name in TEXT_FILENAMES
    ]


def relative(path: Path) -> str:
    return path.relative_to(ROOT).as_posix()


def is_test_or_contract(path: Path) -> bool:
    lowered = {part.lower() for part in path.parts}
    return path.name.endswith("_test.go") or bool(
        lowered & {"test", "tests", "androidtest", "contracts"}
    )


def is_platform_metadata_url(url: str) -> bool:
    return url in {
        "http://schemas.android.com/apk/res/android",
        "http://www.apple.com/DTDs/PropertyList-1.0.dtd",
    }


class MobileContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = json.loads(CONTRACT_PATH.read_text(encoding="utf-8"))

    def test_machine_readable_freeze_is_exact(self) -> None:
        contract = self.contract
        self.assertEqual("yunpin-mobile-protocol-freeze-v1", contract["contract_id"])
        self.assertEqual("frozen", contract["status"])

        wire = contract["wire"]
        self.assertEqual(1, wire["envelope_protocol_version"])
        self.assertEqual(list(range(1, 9)), wire["canonical_header_integer_keys"])
        self.assertEqual(16, wire["identifier_bytes"])
        self.assertEqual(24, wire["nonce_bytes"])
        self.assertEqual(512, wire["padding_bucket_bytes"])
        self.assertEqual(524816, wire["max_ciphertext_bytes"])
        self.assertEqual(
            {
                "magic": "YPBX",
                "version": 1,
                "encoding": "unpadded-base64url",
                "decoded_max_bytes": 262144,
                "framing_bytes": 33,
            },
            wire["sealed_box"],
        )

        credential = contract["credential_bundle"]
        self.assertEqual("YPCB", credential["magic"])
        self.assertEqual(2, credential["write_version"])
        self.assertEqual("platform-secret-store-only", credential["storage"])
        self.assertFalse(credential["recovery_material_present"])
        self.assertFalse(credential["endpoint_present"])

        snapshot = contract["snapshot_tsv"]
        self.assertEqual(
            ["phrase", "pinyin", "source", "use_count", "pinned"],
            snapshot["writer_columns"],
        )
        self.assertEqual(100000, snapshot["max_unique_rows"])
        self.assertEqual("validated-fsync-atomic-replace", snapshot["publication"])
        self.assertFalse(snapshot["bundled_runtime_data"])

    def test_static_scanner_patterns_reject_synthetic_forbidden_examples(self) -> None:
        self.assertIsNotNone(PRIVATE_NETWORK_RE.search("https://192.168.40.20"))
        self.assertIsNotNone(DEPLOYMENT_ALIAS_RE.search("R0W"))
        self.assertIsNotNone(
            ABSOLUTE_USER_PATH_RE.search("/Users/synthetic/private.tsv")
        )
        self.assertIsNotNone(
            ASSIGNED_SERVER_LITERAL_RE.search(
                'serverURL = "https://live-service.invalid"'
            )
        )
        self.assertIsNotNone(DEVICE_CAP_RE.search("MAX_DEVICE_COUNT = 2"))
        self.assertIsNotNone(
            RECOVERY_ENTRYPOINT_PATTERNS["account recovery call"].search(
                "recoverAccount"
            )
        )
        self.assertIsNotNone(
            SECRET_PATTERNS["private-key block"].search(
                "-----BEGIN " + "PRIVATE KEY-----"
            )
        )

    def test_sync_crdt_and_device_policy_are_frozen(self) -> None:
        contract = self.contract
        sync = contract["sync"]
        self.assertEqual("/v1/sync", sync["path"])
        self.assertEqual("persist-exact-wire-before-request", sync["prepared_upload"])
        self.assertEqual("byte-identical", sync["lost_response_retry"])
        self.assertEqual(
            "compare-and-swap-after-durable-merge", sync["cursor_advance"]
        )
        self.assertFalse(sync["empty_page_may_advance_cursor"])

        crdt = contract["crdt"]
        self.assertEqual("per-device-g-counter-component-max", crdt["usage"])
        self.assertEqual("hlc-lww", crdt["pinned"])
        self.assertEqual("remove-wins-within-generation", crdt["presence"])
        self.assertEqual(
            "explicit-readd-next-generation-only", crdt["resurrection"]
        )
        self.assertFalse(crdt["remote_merge_echoes_outbox"])

        devices = contract["device_model"]
        self.assertEqual("dynamic-verified-roster", devices["data_plane"])
        self.assertIsNone(devices["mobile_compiled_device_capacity"])
        self.assertEqual("fixed-two-device-preview", devices["control_plane"])
        self.assertEqual(
            "fail-closed-no-local-mutation", devices["third_device_attempt"]
        )
        self.assertFalse(devices["relay_device_list_is_trust_root"])
        self.assertFalse(devices["server_guard_may_be_changed_to_enable_mobile"])

    def test_component_privacy_recovery_and_human_gates_are_frozen(self) -> None:
        contract = self.contract
        boundary = contract["component_boundary"]
        self.assertEqual(1, boundary["candidate_abi_version"])
        self.assertEqual(
            {
                "password",
                "private-mode",
                "one-time-input",
                "no-personalized-learning",
                "shared-snapshot-unavailable",
            },
            set(boundary["protected_context_flags"]),
        )
        self.assertEqual(
            "zero-private-candidates", boundary["protected_context_result"]
        )
        self.assertEqual("invalid-argument", boundary["unknown_context_flag"])
        self.assertFalse(boundary["keyboard_network_allowed"])
        self.assertFalse(boundary["keyboard_secret_store_allowed"])
        self.assertFalse(boundary["key_event_disk_or_network_wait_allowed"])
        self.assertEqual(
            "no-shared-container-zero-private-candidates",
            boundary["ios_without_full_access"],
        )

        recovery = contract["recovery"]
        self.assertFalse(recovery["enabled"])
        self.assertEqual([], recovery["ui_entrypoints"])
        self.assertEqual([], recovery["network_entrypoints"])
        self.assertFalse(recovery["generate_material"])
        self.assertFalse(recovery["import_material"])
        self.assertFalse(recovery["display_material"])
        self.assertFalse(recovery["silent_account_reset"])

        privacy = contract["repository_privacy"]
        self.assertEqual("synthetic-only", privacy["test_data"])
        for key, value in privacy.items():
            if key != "test_data":
                self.assertFalse(value, key)

        self.assertEqual(
            {
                "developer-signing-material",
                "physical-device-authorization",
                "system-keyboard-or-ime-enablement",
                "ios-full-access",
                "external-publication",
                "live-relay-or-infrastructure-mutation",
                "existing-account-or-roster-migration",
            },
            set(contract["human_gates"]),
        )

    def test_normative_document_covers_every_required_boundary(self) -> None:
        document = FREEZE_DOC_PATH.read_text(encoding="utf-8")
        normalized_document = " ".join(document.split())
        required = (
            "Protocol v1 envelope",
            "YPBX v1 sealed box",
            "YPCB v2 credential bundle",
            "phrase\\tpinyin\\tsource\\tuse_count\\tpinned",
            "prepared-wire retry",
            "compare-and-swap",
            "remove-wins",
            "fixed two-device preview",
            "signed roster-chain",
            "Recovery is out of scope and fail-closed",
            "Containing app and system keyboard boundary",
            "Android uses the native `JobScheduler`",
            "iOS uses BGTask",
            "developer certificates",
            "physical-device trust/authorization",
            "external publishing",
            "live relay",
        )
        for marker in required:
            with self.subTest(marker=marker):
                self.assertIn(marker, normalized_document)

    def test_authoritative_contract_sources_and_mobile_abi_exist(self) -> None:
        for source in self.contract["authoritative_sources"]:
            with self.subTest(source=source):
                self.assertTrue((ROOT / source).is_file(), source)

        header = (MOBILE / "shared" / "include" / "yunpin_mobile_core.h").read_text(
            encoding="utf-8"
        )
        self.assertRegex(header, r"\bYUNPIN_MOBILE_ABI_VERSION\s+1U\b")
        for flag in (
            "YUNPIN_MOBILE_CONTEXT_PASSWORD",
            "YUNPIN_MOBILE_CONTEXT_PRIVATE_MODE",
            "YUNPIN_MOBILE_CONTEXT_ONE_TIME_INPUT",
            "YUNPIN_MOBILE_CONTEXT_NO_PERSONALIZED_LEARNING",
            "YUNPIN_MOBILE_CONTEXT_SHARED_SNAPSHOT_UNAVAILABLE",
        ):
            self.assertIn(flag, header)

        implementation = (
            MOBILE / "shared" / "src" / "yunpin_mobile_core.cpp"
        ).read_text(encoding="utf-8")
        self.assertIn("!parsed.header_valid || parsed.rejected_rows != 0", implementation)

    def test_existing_server_and_credential_guards_remain_fail_closed(self) -> None:
        server = (ROOT / "sync" / "internal" / "server" / "server.go").read_text(
            encoding="utf-8"
        )
        credentials = (ROOT / "desktopagent" / "credentials.go").read_text(
            encoding="utf-8"
        )
        boxes = (ROOT / "protocol" / "boxes.go").read_text(encoding="utf-8")
        snapshot = (ROOT / "desktopagent" / "snapshot.go").read_text(
            encoding="utf-8"
        )

        self.assertRegex(server, r"\bprotocolVersion\s*=\s*1\b")
        self.assertRegex(server, r"\bmaxActiveDevices\s*=\s*2\b")
        self.assertRegex(server, r"\btwoDeviceRecoveryEnabled\s*=\s*false\b")
        self.assertRegex(server, r"\btwoDeviceRevocationEnabled\s*=\s*false\b")
        self.assertIn('"device_limit_reached"', server)
        self.assertIn(
            '"recovery_not_available_in_two_device_preview"', server
        )

        self.assertRegex(credentials, r"\bCredentialBundleVersion\s*=\s*2\b")
        self.assertIn("{'Y', 'P', 'C', 'B'}", credentials)
        self.assertGreaterEqual(credentials.count("len(bundle.TrustedRoster.Devices) != 2"), 1)
        self.assertIn("supports exactly two", credentials)

        self.assertRegex(boxes, r"\bSealedBoxWireVersion\s*=\s*1\b")
        self.assertIn("{'Y', 'P', 'B', 'X'}", boxes)
        self.assertIn(
            'privateSnapshotHeader  = "phrase\\tpinyin\\tsource\\tuse_count\\tpinned\\n"',
            snapshot,
        )

    def test_mobile_tree_contains_no_private_or_recorded_data_files(self) -> None:
        offenders: list[str] = []
        for path in iter_mobile_files():
            name = path.name.lower()
            suffix = path.suffix.lower()
            if name in FORBIDDEN_DATA_NAMES or suffix in FORBIDDEN_DATA_SUFFIXES:
                offenders.append(relative(path))
            if name.endswith(".userdb.kct.snapshot") or name.endswith(".scel.bin"):
                offenders.append(relative(path))
        self.assertEqual([], sorted(set(offenders)))

    def test_mobile_contracts_and_sources_contain_no_deployment_identity(self) -> None:
        offenders: list[str] = []
        for path in iter_mobile_text_files():
            text = path.read_text(encoding="utf-8")
            synthetic_fixture = is_test_or_contract(path) and "synthetic" in text.lower()
            if not is_test_or_contract(path):
                for match in ASSIGNED_SERVER_LITERAL_RE.finditer(text):
                    line_number = text.count("\n", 0, match.start()) + 1
                    offenders.append(
                        f"{relative(path)}:{line_number}: assigned server literal"
                    )
            for line_number, line in enumerate(text.splitlines(), start=1):
                if PRIVATE_NETWORK_RE.search(line) and not synthetic_fixture:
                    offenders.append(f"{relative(path)}:{line_number}: private IP")
                if DEPLOYMENT_ALIAS_RE.search(line):
                    offenders.append(f"{relative(path)}:{line_number}: deployment alias")
                if ABSOLUTE_USER_PATH_RE.search(line):
                    offenders.append(f"{relative(path)}:{line_number}: absolute user path")

                for match in re.finditer(r"https?://[^\s\"'<>]+", line):
                    url = match.group(0).rstrip("),.;")
                    if is_platform_metadata_url(url):
                        continue
                    host = (urlsplit(url).hostname or "").lower()
                    if is_test_or_contract(path) and (
                        host.endswith(".invalid") or host.endswith(".test")
                    ):
                        continue
                    try:
                        address = ipaddress.ip_address(host)
                    except ValueError:
                        address = None
                    if synthetic_fixture and address is not None and (
                        address.is_private
                        or address in ipaddress.ip_network("192.0.2.0/24")
                        or address in ipaddress.ip_network("198.51.100.0/24")
                        or address in ipaddress.ip_network("203.0.113.0/24")
                    ):
                        continue
                    offenders.append(
                        f"{relative(path)}:{line_number}: compiled URL {url}"
                    )
        self.assertEqual([], offenders)

    def test_mobile_sources_contain_no_secret_literals(self) -> None:
        offenders: list[str] = []
        for path in iter_mobile_text_files():
            text = path.read_text(encoding="utf-8")
            for label, pattern in SECRET_PATTERNS.items():
                if pattern.search(text):
                    offenders.append(f"{relative(path)}: {label}")
            for match in ASSIGNED_SECRET_RE.finditer(text):
                literal = match.group(1).lower()
                if not any(
                    marker in literal
                    for marker in ("synthetic", "example", "invalid", "test-only")
                ):
                    line_number = text.count("\n", 0, match.start()) + 1
                    offenders.append(
                        f"{relative(path)}:{line_number}: assigned secret literal"
                    )
        self.assertEqual([], offenders)

    def test_mobile_runtime_does_not_compile_a_device_capacity_or_recovery_flow(self) -> None:
        offenders: list[str] = []
        runtime_roots = [
            MOBILE / "android",
            MOBILE / "ios",
            MOBILE / "synccore",
            MOBILE / "shared",
        ]
        for runtime_root in runtime_roots:
            if not runtime_root.exists():
                continue
            for path in sorted(runtime_root.rglob("*")):
                if (
                    not path.is_file()
                    or path.suffix.lower() not in TEXT_SUFFIXES
                    or any(part in SKIP_PARTS for part in path.parts)
                ):
                    continue
                text = path.read_text(encoding="utf-8")
                if DEVICE_CAP_RE.search(text):
                    offenders.append(f"{relative(path)}: compiled device capacity")
                for label, pattern in RECOVERY_ENTRYPOINT_PATTERNS.items():
                    if pattern.search(text):
                        offenders.append(f"{relative(path)}: {label}")
        self.assertEqual([], offenders)

        sync_core = (MOBILE / "synccore" / "core.go").read_text(encoding="utf-8")
        self.assertIn('ControlPlaneGate: "signed_roster_chain_required"', sync_core)
        self.assertIn("It never invokes account creation, recovery or pairing.", sync_core)

        snapshot_core = (MOBILE / "synccore" / "snapshot.go").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            'mobileSnapshotHeader = "phrase\\tpinyin\\tsource\\tuse_count\\tpinned\\n"',
            snapshot_core,
        )
        self.assertNotIn("pinned\\tlast_used_day\\n", snapshot_core)

    def test_keyboard_and_ime_sources_have_no_network_or_secret_store_access(self) -> None:
        roots = [
            MOBILE
            / "android"
            / "app"
            / "src"
            / "main"
            / "java"
            / "io"
            / "github"
            / "kukuyan"
            / "yunpin"
            / "android"
            / "ime",
            MOBILE / "ios" / "KeyboardExtension",
            MOBILE / "ios" / "Sources" / "YunPinKeyboardCore",
        ]
        forbidden = {
            "network API": re.compile(
                r"(?i)\b(?:URLSession|URLRequest|NWConnection|HttpURLConnection|"
                r"OkHttpClient|Retrofit|java\.net|io\.ktor\.client|Socket)\b"
            ),
            "relay API": re.compile(
                r"(?i)(?:/v1/|\b(?:RelayClient|SyncClient|SyncEndpoint|EndpointProfile)\b)"
            ),
            "secret store": re.compile(
                r"(?i)\b(?:Keychain|KeyStore|SecItem|device[_A-Z]?token|"
                r"signing[_A-Z]?(?:seed|private)|x25519[_A-Z]?private)\b"
            ),
        }
        offenders: list[str] = []
        for root in roots:
            if not root.exists():
                continue
            for path in sorted(root.rglob("*")):
                if not path.is_file() or path.suffix.lower() not in {
                    ".h",
                    ".java",
                    ".kt",
                    ".swift",
                }:
                    continue
                text = path.read_text(encoding="utf-8")
                for label, pattern in forbidden.items():
                    if pattern.search(text):
                        offenders.append(f"{relative(path)}: {label}")
        self.assertEqual([], offenders)


if __name__ == "__main__":
    unittest.main()
