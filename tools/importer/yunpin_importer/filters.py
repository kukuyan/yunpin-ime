# SPDX-License-Identifier: Apache-2.0
"""Privacy filters shared by every importer.

The filters intentionally prefer false positives over leaking a credential or
an original chat sentence into a generated personal dictionary.
"""

from __future__ import annotations

import hashlib
import re
import unicodedata
from collections import Counter
from typing import Callable, Iterable, Iterator, Optional, Tuple


CJK = r"\u3400-\u4dbf\u4e00-\u9fff"
CJK_RUN = re.compile(rf"[{CJK}]{{2,96}}")

_PATTERNS = (
    ("url", re.compile(r"(?i)(?:https?|ftp)://|\bwww\.")),
    ("email", re.compile(r"(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b")),
    ("domain", re.compile(r"(?i)\b(?:[A-Z0-9-]+\.)+(?:com|net|org|cn|io|dev|app|local|test)\b")),
    (
        "ipv4",
        re.compile(
            r"(?<!\d)(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}"
            r"(?:25[0-5]|2[0-4]\d|1?\d?\d)(?!\d)"
        ),
    ),
    ("ipv6", re.compile(r"(?i)(?<![0-9a-f])(?:[0-9a-f]{1,4}:){2,}[0-9a-f:]{0,39}(?![0-9a-f])")),
    ("windows_path", re.compile(r"(?i)(?:\b[A-Z]:\\|\\\\[^\\\s]+\\)")),
    (
        "unix_path",
        re.compile(r"(?:^|\s)/(?:Users|home|root|etc|var|tmp|opt|srv|mnt|Volumes)(?:/[^\s]*)?"),
    ),
    ("file_path", re.compile(r"(?:[/\\][^\s/\\]+){1,}")),
    ("long_number", re.compile(r"(?<!\d)(?:\d{6,}|(?:\+?\d[\s().-]*){7,})(?!\d)")),
    (
        "opaque_token",
        re.compile(
            r"(?i)\b(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|"
            r"[A-Za-z0-9_-]{24,})\b"
        ),
    ),
    ("jwt", re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b")),
    (
        "secret",
        re.compile(
            r"(?i)(?:-----BEGIN [A-Z ]*PRIVATE KEY-----|\bBearer\s+[A-Za-z0-9._~-]+|"
            r"\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|password|passwd|secret)"
            r"(?:\s*[:=]\s*|\s+)\S+|\b(?:gh[pousr]_|sk-)[A-Za-z0-9_-]{8,}|"
            r"\bAKIA[A-Z0-9]{12,}\b|(?:密码|口令|访问令牌|刷新令牌|私钥|密钥)"
            r"(?:\s*[:：=]\s*|\s+)\S+)"
        ),
    ),
)

_CODE_FENCE = re.compile(r"```.*?```|~~~.*?~~~", re.DOTALL)
_INLINE_CODE = re.compile(r"`[^`\n]+`")
_MARKDOWN_LINK = re.compile(r"!?(?:\[[^\]]*\])\([^\s)]+\)")
_ORG_PATTERN = re.compile(
    rf"([{CJK}]{{1,64}}?(?:股份有限公司[{CJK}]{{0,18}}分公司|集团有限公司|"
    rf"有限责任公司|有限公司|分公司|研究院|研究所|委员会|管理局|"
    rf"服务中心|数据中心|大学|学院|银行|医院))"
)
_LEADING_CHAT = re.compile(
    r"^(?:请你|请帮我|请记住|请|帮我|帮忙|我想|我们想|我们需要|我需要|我要|我们要|"
    r"麻烦|关于|记住|可以|是否可以|目前|现在|今天|明天|昨天|然后|接下来|这个|那个|"
    r"这是|那是|这里|那里|讨论|处理|联系|查看|使用|部署|安装|打开)+"
)
_SENTENCE_ENDINGS = tuple("吗呢吧啊呀嘛呗啦哇哦的了着过就也都才会能要想请")
_PREDICATE_ENDINGS = (
    "真好",
    "很好",
    "不好",
    "天晴",
    "成功",
    "失败",
    "完成",
    "正常",
    "异常",
    "可以",
    "不行",
)
_RAW_SENTENCE_MARKERS = re.compile(
    r"(?:我|你|他|她|它|我们|你们|他们|请|需要|必须|不得|不要|应该|可以|是否|"
    r"怎么|如何|麻烦|按照|立即|马上|然后|接下来|正在|已经|仍然|希望|觉得|认为)"
)
_ENTITY_DISQUALIFIERS = re.compile(
    r"(?:喜欢|检查|查看|使用|安装|打开|关闭|升级|部署|处理|修改|创建|删除|运行|"
    r"启动|停止|修复|访问|连接|保存|写入|读取)"
)
_FORMAL_ENTITY_SUFFIXES = (
    "股份有限公司",
    "集团有限公司",
    "有限责任公司",
    "有限公司",
    "分公司",
    "研究院",
    "研究所",
    "实验室",
    "委员会",
    "管理局",
    "服务中心",
    "数据中心",
    "中心",
    "大学",
    "学院",
    "银行",
    "医院",
)
_DOMAIN_ENTITY_SUFFIXES = (
    "输入法",
    "候选窗",
    "候选词",
    "个人词库",
    "公共词库",
    "词库",
    "云同步",
    "同步服务",
    "客户端",
    "服务端",
    "服务器",
    "数据库",
    "路由器",
    "交换机",
    "网络运维",
    "语音服务",
    "恢复密钥",
    "加密同步",
    "网关",
    "协议",
    "平台",
    "系统",
    "模型",
    "算法",
    "框架",
    "引擎",
    "插件",
    "容器",
    "集群",
    "项目",
    "工具",
    "配置",
)


def unsafe_reason(value: str) -> Optional[str]:
    normalized = unicodedata.normalize("NFKC", value)
    for reason, pattern in _PATTERNS:
        if pattern.search(normalized):
            return reason
    return None


def strip_code_and_links(text: str) -> str:
    text = _CODE_FENCE.sub("\n", text)
    text = _INLINE_CODE.sub(" ", text)
    text = _MARKDOWN_LINK.sub(" ", text)
    kept = []
    for line in text.splitlines():
        if line.startswith(("    ", "\t")):
            continue
        kept.append(line)
    return "\n".join(kept)


def normalize_phrase(value: str) -> str:
    value = unicodedata.normalize("NFKC", value).strip()
    value = re.sub(r"\s+", "", value)
    return value


def normalize_pinyin(value: str) -> str:
    value = unicodedata.normalize("NFKC", value).lower().strip()
    value = value.replace("u:", "v")
    tone_u = str.maketrans(
        {
            "ü": "v",
            "ǖ": "v",
            "ǘ": "v",
            "ǚ": "v",
            "ǜ": "v",
        }
    )
    value = value.translate(tone_u)
    value = "".join(ch for ch in unicodedata.normalize("NFD", value) if not unicodedata.combining(ch))
    value = re.sub(r"[1-5]", "", value)
    value = value.replace("'", " ")
    value = re.sub(r"[^a-zv\s]", " ", value)
    return re.sub(r"\s+", " ", value).strip()


def validate_phrase(value: str) -> Tuple[bool, Optional[str]]:
    if not value:
        return False, "empty"
    reason = unsafe_reason(value)
    if reason:
        return False, reason
    if len(value) > 96:
        return False, "too_long"
    if not re.search(rf"[{CJK}]", value):
        return False, "non_cjk"
    if re.search(r"[\r\n\t]", value):
        return False, "control_character"
    return True, None


def _trim_chat_prefix(value: str) -> str:
    previous = None
    while value and value != previous:
        previous = value
        value = _LEADING_CHAT.sub("", value)
    return value


def _looks_sentence_like(value: str) -> bool:
    if _RAW_SENTENCE_MARKERS.search(value):
        return True
    if value.endswith(_PREDICATE_ENDINGS):
        return True
    return len(value) > 4 and value.endswith(_SENTENCE_ENDINGS)


def _is_explicit_entity(value: str) -> bool:
    if _ENTITY_DISQUALIFIERS.search(value):
        return False
    if len(value) >= 4 and value.endswith(_FORMAL_ENTITY_SUFFIXES):
        return True
    return len(value) >= 3 and value.endswith(_DOMAIN_ENTITY_SUFFIXES)


def extract_history_terms(
    texts: Iterable[str],
    rejected: Optional[Counter] = None,
    known_term: Optional[Callable[[str], bool]] = None,
) -> Iterator[str]:
    """Yield terms, never full prose/code blocks.

    A CJK fragment must be an exact member of a supplied terminology dictionary
    or match a narrow entity suffix. Character-by-character pronunciation is
    deliberately not accepted as proof that a fragment is a term.
    """
    for raw in texts:
        cleaned = strip_code_and_links(raw)
        for line in cleaned.splitlines():
            reason = unsafe_reason(line)
            if reason:
                if rejected is not None:
                    rejected[reason] += 1
                continue
            for match in CJK_RUN.finditer(line):
                run = _trim_chat_prefix(match.group(0))
                if len(run) < 2:
                    continue
                if _looks_sentence_like(run):
                    if rejected is not None:
                        rejected["sentence_like"] += 1
                    continue
                if len(run) <= 32:
                    if _is_explicit_entity(run) or (known_term is not None and known_term(run)):
                        yield run
                    elif rejected is not None:
                        rejected["not_explicit_term"] += 1
                    continue
                organizations = 0
                for organization in _ORG_PATTERN.finditer(run):
                    phrase = _trim_chat_prefix(organization.group(1))
                    if 4 <= len(phrase) <= 64 and not _looks_sentence_like(phrase) and _is_explicit_entity(phrase):
                        organizations += 1
                        yield phrase
                if organizations == 0 and rejected is not None:
                    rejected["raw_sentence"] += 1


def masked_phrase(value: str) -> str:
    if not value:
        return ""
    digest = hashlib.sha256(value.encode("utf-8")).hexdigest()[:8]
    if len(value) == 1:
        visible = "*"
    elif len(value) == 2:
        visible = value[0] + "*"
    else:
        visible = value[0] + ("*" * min(6, len(value) - 2)) + value[-1]
    return f"{visible} (len={len(value)}, sha256={digest})"
