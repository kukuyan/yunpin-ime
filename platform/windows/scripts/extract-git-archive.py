#!/usr/bin/env python3
"""Extract a Git-produced tar without platform locale-dependent filenames."""
import shutil
import sys
import tarfile
from pathlib import Path, PurePosixPath


def extract(archive: Path, destination: Path, strip: int) -> None:
    if strip < 0 or not destination.is_dir() or any(destination.iterdir()):
        raise ValueError("destination must be an empty directory and strip nonnegative")
    seen = set()
    with tarfile.open(archive, mode="r:", encoding="utf-8", errors="strict") as tf:
        for member in tf:
            name = member.name
            parts = PurePosixPath(name).parts
            if not parts or name.startswith("/") or any(
                part in (".", "..") or "\\" in part or ":" in part for part in parts
            ):
                raise ValueError(f"unsafe archive path: {name!r}")
            if not (member.isdir() or member.isfile()):
                raise ValueError(f"unsupported archive entry: {name!r}")
            if len(parts) <= strip:
                if not member.isdir():
                    raise ValueError(f"cannot strip file path: {name!r}")
                continue
            relative = parts[strip:]
            key = "/".join(relative).casefold()
            if key in seen:
                raise ValueError(f"duplicate archive path: {name!r}")
            seen.add(key)
            target = destination.joinpath(*relative)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with tf.extractfile(member) as src, target.open("xb") as out:
                    shutil.copyfileobj(src, out)


if __name__ == "__main__":
    if len(sys.argv) != 4:
        raise SystemExit("usage: extract-git-archive.py ARCHIVE EMPTY_DESTINATION STRIP")
    extract(Path(sys.argv[1]), Path(sys.argv[2]), int(sys.argv[3]))
