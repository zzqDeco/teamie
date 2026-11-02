#!/usr/bin/env python3
"""Confined Sandbox filesystem archiving, checksum verification, and safe restore."""
import argparse
import gzip
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import shutil
import stat
import sys
import tarfile

DEFAULT_MIN_FREE_BYTES = 2 * 1024 * 1024 * 1024  # 2 GiB reserve
DEFAULT_MAX_ARCHIVE_BYTES = 4 * 1024 * 1024 * 1024  # 4 GiB ceiling
MAX_ARCHIVE_ENTRIES = 200_000
CHUNK_BYTES = 65536
CHECKSUM_LINE_RE = re.compile(r"^([0-9a-f]{64}) [ \*](?:\./)?([A-Za-z0-9._-]+)$")
METADATA_FILES = ("release-manifest.json", "deployment.json", "backup-manifest.json")


def fail(message: str) -> "NoReturn":  # type: ignore[name-defined]
    raise ValueError(message)


def open_nofollow_rdonly(path: str) -> int:
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    if hasattr(os, "O_NONBLOCK"):
        flags |= os.O_NONBLOCK
    return os.open(path, flags)


def sha256_file(path: Path) -> str:
    st = os.lstat(path)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode):
        fail(f"refusing to hash non-regular file: {path.name}")
    fd = open_nofollow_rdonly(str(path))
    digest = hashlib.sha256()
    try:
        fst = os.fstat(fd)
        if not stat.S_ISREG(fst.st_mode) or (fst.st_dev, fst.st_ino) != (st.st_dev, st.st_ino):
            fail(f"concurrent file replacement detected while hashing: {path.name}")
        while True:
            chunk = os.read(fd, CHUNK_BYTES)
            if not chunk:
                break
            digest.update(chunk)
    finally:
        os.close(fd)
    return digest.hexdigest()


def check_space(target_path: str, min_bytes: int) -> dict:
    if min_bytes < 0:
        fail("min-bytes must be non-negative")
    probe = Path(target_path)
    while not probe.exists():
        if probe.parent == probe:
            break
        probe = probe.parent
    usage = shutil.disk_usage(str(probe))
    if usage.free < min_bytes:
        fail(
            f"insufficient free disk space at {probe}: "
            f"{usage.free} bytes available, require at least {min_bytes} bytes"
        )
    return {"path": str(probe), "freeBytes": usage.free, "minBytes": min_bytes}


def validate_rel_path(raw_name: str) -> str:
    if not isinstance(raw_name, str) or "\x00" in raw_name or "\\" in raw_name:
        fail("archive member contains invalid path characters")
    trimmed = raw_name
    while trimmed.startswith("./"):
        trimmed = trimmed[2:]
    trimmed = trimmed.rstrip("/")
    if trimmed in ("", "."):
        return ""
    if trimmed.startswith("/") or len(trimmed) > 4096:
        fail(f"archive member escapes target root: {raw_name!r}")
    parts = trimmed.split("/")
    for part in parts:
        if not part or part in (".", ".."):
            fail(f"archive member contains unsafe path segment: {raw_name!r}")
    return "/".join(parts)


def _walk_confined_entries(root_dir: Path):
    root_stat = os.lstat(root_dir)
    if stat.S_ISLNK(root_stat.st_mode) or not stat.S_ISDIR(root_stat.st_mode):
        fail(f"sandbox root must be a real non-symlink directory: {root_dir}")

    stack = [("", root_dir)]
    while stack:
        rel_prefix, current_dir = stack.pop()
        with os.scandir(current_dir) as it:
            entries = sorted(it, key=lambda e: e.name)
        subdirs = []
        for entry in entries:
            rel = f"{rel_prefix}/{entry.name}" if rel_prefix else entry.name
            validate_rel_path(rel)
            st = os.lstat(entry.path)
            mode = st.st_mode
            if stat.S_ISLNK(mode):
                fail(f"symlink entry rejected in sandbox archive: {rel}")
            if stat.S_ISDIR(mode):
                yield rel, Path(entry.path), st, True
                subdirs.append((rel, Path(entry.path)))
            elif stat.S_ISREG(mode):
                yield rel, Path(entry.path), st, False
            else:
                fail(f"unsupported special file rejected in sandbox archive: {rel}")
        for item in reversed(subdirs):
            stack.append(item)


def write_tar_gz_stream(source_dir: Path, out_stream, allow_missing: bool = False) -> dict:
    if not source_dir.exists():
        if not allow_missing:
            fail(f"sandbox source directory does not exist: {source_dir}")
        with gzip.GzipFile(filename="", mode="wb", fileobj=out_stream, mtime=0) as gz:
            with tarfile.open(mode="w", fileobj=gz, format=tarfile.PAX_FORMAT) as tf:
                root_info = tarfile.TarInfo(name=".")
                root_info.type = tarfile.DIRTYPE
                root_info.mode = 0o700
                root_info.uid = 0
                root_info.gid = 0
                root_info.uname = ""
                root_info.gname = ""
                root_info.mtime = 0
                tf.addfile(root_info)
        return {"exists": False, "entries": 0, "files": 0, "bytes": 0}

    entry_count = 0
    file_count = 0
    total_bytes = 0
    with gzip.GzipFile(filename="", mode="wb", fileobj=out_stream, mtime=0) as gz:
        with tarfile.open(mode="w", fileobj=gz, format=tarfile.PAX_FORMAT) as tf:
            for rel, full_path, st, is_dir in _walk_confined_entries(source_dir):
                entry_count += 1
                if entry_count > MAX_ARCHIVE_ENTRIES:
                    fail("sandbox archive entry count exceeded safety bound")
                info = tarfile.TarInfo(name=rel)
                info.uid = 0
                info.gid = 0
                info.uname = ""
                info.gname = ""
                info.mtime = int(st.st_mtime)
                info.mode = st.st_mode & 0o777
                if is_dir:
                    info.type = tarfile.DIRTYPE
                    info.size = 0
                    tf.addfile(info)
                    continue

                info.type = tarfile.REGTYPE
                info.size = st.st_size
                total_bytes += st.st_size
                if total_bytes > DEFAULT_MAX_ARCHIVE_BYTES:
                    fail("sandbox archive byte size exceeded safety bound")

                fd = open_nofollow_rdonly(str(full_path))
                try:
                    fst = os.fstat(fd)
                    if (
                        not stat.S_ISREG(fst.st_mode)
                        or (fst.st_dev, fst.st_ino) != (st.st_dev, st.st_ino)
                        or fst.st_size != st.st_size
                    ):
                        fail(f"concurrent modification detected while archiving: {rel}")
                    with os.fdopen(fd, "rb", closefd=False) as reader:
                        tf.addfile(info, fileobj=reader)
                finally:
                    os.close(fd)
                file_count += 1

    return {
        "exists": True,
        "entries": entry_count,
        "files": file_count,
        "bytes": total_bytes,
    }


def write_atomic_archive(source_dir: Path, target_archive: Path, allow_missing: bool) -> dict:
    part_path = target_archive.parent / f".{target_archive.name}.part.{os.getpid()}"
    fd = os.open(str(part_path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as handle:
            summary = write_tar_gz_stream(source_dir, handle, allow_missing=allow_missing)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(part_path, target_archive)
    except Exception:
        try:
            part_path.unlink()
        except OSError:
            pass
        raise
    summary["archive"] = target_archive.name
    summary["sha256"] = sha256_file(target_archive)
    return summary


def inspect_and_validate_archive(archive_path: Path, max_bytes: int = DEFAULT_MAX_ARCHIVE_BYTES) -> dict:
    st = os.lstat(archive_path)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode) or st.st_size == 0:
        fail(f"invalid or empty archive file: {archive_path}")

    seen = set()
    entries = 0
    files = 0
    total_bytes = 0
    try:
        with tarfile.open(archive_path, mode="r:gz") as tf:
            for member in tf:
                if member.issym() or member.islnk():
                    fail(f"archive contains forbidden link entry: {member.name!r}")
                if not (member.isdir() or member.isfile()):
                    fail(f"archive contains unsupported special entry: {member.name!r}")
                rel = validate_rel_path(member.name)
                if not rel:
                    continue
                if rel in seen:
                    fail(f"duplicate archive member path: {rel}")
                seen.add(rel)
                entries += 1
                if entries > MAX_ARCHIVE_ENTRIES:
                    fail("archive exceeds maximum allowed entry count")
                if member.isfile():
                    if member.size < 0:
                        fail(f"negative member size in archive: {rel}")
                    total_bytes += member.size
                    if total_bytes > max_bytes:
                        fail("archive uncompressed total exceeds safety limit")
                    reader = tf.extractfile(member)
                    if reader is None:
                        fail(f"unable to read regular member stream: {rel}")
                    read_bytes = 0
                    while True:
                        chunk = reader.read(CHUNK_BYTES)
                        if not chunk:
                            break
                        read_bytes += len(chunk)
                    if read_bytes != member.size:
                        fail(f"truncated member stream in archive: {rel}")
                    files += 1
    except (tarfile.TarError, EOFError, OSError) as exc:
        fail(f"corrupt or unreadable tar.gz archive: {exc}")

    return {
        "entries": entries,
        "files": files,
        "bytes": total_bytes,
        "sha256": sha256_file(archive_path),
    }


def _assert_confined_no_symlinks(target_root: Path, candidate: Path) -> None:
    root_str = str(target_root)
    cand_str = str(candidate)
    if os.path.commonpath([root_str, cand_str]) != root_str:
        fail(f"restore path escaped target root: {candidate}")
    rel = candidate.relative_to(target_root)
    cur = target_root
    for part in rel.parts[:-1]:
        cur = cur / part
        if cur.exists() or cur.is_symlink():
            st = os.lstat(cur)
            if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
                fail(f"non-directory or symlink parent encountered during restore: {cur}")


def restore_archive(
    archive_path: Path,
    target_dir: Path,
    root_mode: int = 0o700,
    max_bytes: int = DEFAULT_MAX_ARCHIVE_BYTES,
) -> dict:
    summary = inspect_and_validate_archive(archive_path, max_bytes=max_bytes)

    if target_dir.exists() or target_dir.is_symlink():
        st = os.lstat(target_dir)
        if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
            fail(f"restore target must be a real directory: {target_dir}")
        if os.listdir(target_dir):
            fail(f"refusing to restore into non-empty target directory: {target_dir}")
        os.chmod(target_dir, root_mode & 0o777)
    else:
        target_dir.mkdir(parents=True, exist_ok=False, mode=root_mode & 0o777)
        os.chmod(target_dir, root_mode & 0o777)

    target_real = Path(os.path.realpath(target_dir))
    dir_modes: list[tuple[Path, int]] = []

    with tarfile.open(archive_path, mode="r:gz") as tf:
        for member in tf:
            rel = validate_rel_path(member.name)
            if not rel:
                continue
            dest = target_real / rel
            _assert_confined_no_symlinks(target_real, dest)
            parent = dest.parent
            if parent != target_real and not parent.exists():
                parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            _assert_confined_no_symlinks(target_real, dest)

            safe_mode = member.mode & 0o777
            if member.isdir():
                if not dest.exists():
                    dest.mkdir(mode=0o700, exist_ok=False)
                dir_modes.append((dest, safe_mode if safe_mode else 0o700))
                continue

            open_flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
            if hasattr(os, "O_NOFOLLOW"):
                open_flags |= os.O_NOFOLLOW
            file_mode = safe_mode if safe_mode else 0o600
            fd = os.open(str(dest), open_flags, file_mode)
            try:
                os.fchmod(fd, file_mode)
                reader = tf.extractfile(member)
                if reader is None:
                    fail(f"missing data stream for {rel}")
                written = 0
                while True:
                    chunk = reader.read(CHUNK_BYTES)
                    if not chunk:
                        break
                    os.write(fd, chunk)
                    written += len(chunk)
                if written != member.size:
                    fail(f"short write while restoring {rel}")
                os.fsync(fd)
            finally:
                os.close(fd)
            os.utime(dest, (member.mtime, member.mtime), follow_symlinks=False)

    for directory, desired_mode in sorted(dir_modes, key=lambda item: len(item[0].parts), reverse=True):
        os.chmod(directory, desired_mode)
    os.chmod(target_real, root_mode & 0o777)
    return summary


def _discover_backup_artifacts(backup_dir: Path) -> tuple[list[str], int]:
    if not backup_dir.is_dir() or backup_dir.is_symlink():
        fail(f"backup directory must be a real directory: {backup_dir}")
    names: list[str] = []
    primary_count = 0
    for entry in sorted(os.listdir(backup_dir)):
        if entry == "SHA256SUMS" or entry.startswith("."):
            continue
        is_primary = entry.endswith(".dump") or (
            entry.startswith("sandbox-") and entry.endswith(".tar.gz")
        )
        is_meta = entry in METADATA_FILES
        if is_primary or is_meta:
            full = backup_dir / entry
            st = os.lstat(full)
            if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode):
                fail(f"backup artifact is not a regular file: {entry}")
            names.append(entry)
            if is_primary:
                primary_count += 1
    return names, primary_count


def write_checksums(backup_dir: Path) -> dict:
    names, primary_count = _discover_backup_artifacts(backup_dir)
    if primary_count == 0:
        fail(f"no .dump or sandbox-*.tar.gz artifacts found in {backup_dir}")
    lines = []
    for name in names:
        digest = sha256_file(backup_dir / name)
        lines.append(f"{digest}  ./{name}\n")
    sums_path = backup_dir / "SHA256SUMS"
    part_path = backup_dir / f".SHA256SUMS.part.{os.getpid()}"
    fd = os.open(str(part_path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.writelines(lines)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(part_path, sums_path)
    except Exception:
        try:
            part_path.unlink()
        except OSError:
            pass
        raise
    return {"files": names, "primaryArtifacts": primary_count}


def verify_checksums(backup_dir: Path) -> dict:
    sums_path = backup_dir / "SHA256SUMS"
    if not sums_path.exists():
        fail(f"missing SHA256SUMS in {backup_dir}")
    st = os.lstat(sums_path)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode) or st.st_size == 0:
        fail(f"invalid or empty SHA256SUMS in {backup_dir}")

    raw_lines = sums_path.read_text(encoding="utf-8").splitlines()
    if not raw_lines:
        fail("SHA256SUMS contains no entries")

    recorded: dict[str, str] = {}
    for line in raw_lines:
        match = CHECKSUM_LINE_RE.fullmatch(line.strip())
        if not match:
            fail(f"malformed SHA256SUMS entry: {line!r}")
        digest, filename = match.group(1), match.group(2)
        if filename in recorded:
            fail(f"duplicate filename in SHA256SUMS: {filename}")
        recorded[filename] = digest

    discovered_names, primary_count = _discover_backup_artifacts(backup_dir)
    if primary_count == 0:
        fail(f"no primary backup artifacts (.dump / sandbox-*.tar.gz) present in {backup_dir}")
    missing_from_manifest = sorted(set(discovered_names) - set(recorded))
    if missing_from_manifest:
        fail(f"unverified backup artifacts missing from SHA256SUMS: {', '.join(missing_from_manifest)}")

    for filename, expected_digest in sorted(recorded.items()):
        target = backup_dir / filename
        if not target.exists():
            fail(f"artifact listed in SHA256SUMS is missing: {filename}")
        actual_digest = sha256_file(target)
        if not hmac.compare_digest(actual_digest, expected_digest):
            fail(f"SHA-256 mismatch for {filename}: expected {expected_digest}, got {actual_digest}")

    return {"verifiedFiles": sorted(recorded), "primaryArtifacts": primary_count}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    space_cmd = sub.add_parser("check-space")
    space_cmd.add_argument("--path", required=True)
    space_cmd.add_argument("--min-bytes", type=int, default=DEFAULT_MIN_FREE_BYTES)

    archive_cmd = sub.add_parser("archive")
    archive_cmd.add_argument("--workspaces", required=True)
    archive_cmd.add_argument("--state", required=True)
    archive_cmd.add_argument("--out-dir", required=True)
    archive_cmd.add_argument("--allow-missing", action="store_true")

    stream_cmd = sub.add_parser("stream-archive")
    stream_cmd.add_argument("--source", required=True)
    stream_cmd.add_argument("--allow-missing", action="store_true")

    verify_cmd = sub.add_parser("verify-archive")
    verify_cmd.add_argument("--archive", required=True)
    verify_cmd.add_argument("--max-bytes", type=int, default=DEFAULT_MAX_ARCHIVE_BYTES)

    restore_cmd = sub.add_parser("restore")
    restore_cmd.add_argument("--archive", required=True)
    restore_cmd.add_argument("--target", required=True)
    restore_cmd.add_argument("--root-mode", default="0700")
    restore_cmd.add_argument("--max-bytes", type=int, default=DEFAULT_MAX_ARCHIVE_BYTES)

    cs_write = sub.add_parser("checksums-write")
    cs_write.add_argument("--dir", required=True)

    cs_verify = sub.add_parser("checksums-verify")
    cs_verify.add_argument("--dir", required=True)

    args = parser.parse_args()
    try:
        if args.command == "check-space":
            print(json.dumps(check_space(args.path, args.min_bytes), sort_keys=True))
        elif args.command == "archive":
            out_dir = Path(args.out_dir)
            if not out_dir.is_dir() or out_dir.is_symlink():
                fail(f"output directory must be an existing directory: {out_dir}")
            ws_summary = write_atomic_archive(
                Path(args.workspaces),
                out_dir / "sandbox-workspaces.tar.gz",
                allow_missing=args.allow_missing,
            )
            st_summary = write_atomic_archive(
                Path(args.state),
                out_dir / "sandbox-state.tar.gz",
                allow_missing=args.allow_missing,
            )
            print(json.dumps({"workspaces": ws_summary, "state": st_summary}, sort_keys=True))
        elif args.command == "stream-archive":
            write_tar_gz_stream(Path(args.source), sys.stdout.buffer, allow_missing=args.allow_missing)
            sys.stdout.buffer.flush()
        elif args.command == "verify-archive":
            print(json.dumps(inspect_and_validate_archive(Path(args.archive), max_bytes=args.max_bytes), sort_keys=True))
        elif args.command == "restore":
            root_mode = int(args.root_mode, 8)
            print(
                json.dumps(
                    restore_archive(
                        Path(args.archive),
                        Path(args.target),
                        root_mode=root_mode,
                        max_bytes=args.max_bytes,
                    ),
                    sort_keys=True,
                )
            )
        elif args.command == "checksums-write":
            print(json.dumps(write_checksums(Path(args.dir)), sort_keys=True))
        elif args.command == "checksums-verify":
            print(json.dumps(verify_checksums(Path(args.dir)), sort_keys=True))
    except (ValueError, OSError) as exc:
        print(f"sandbox_archive error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
