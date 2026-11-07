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
MAX_ARCHIVE_DEPTH = 64
SANDBOX_RUNNER_UID = 10001
SANDBOX_RUNNER_GID = 10001
VALID_RESTORE_SCOPES = ("auto", "workspaces", "state")
CHUNK_BYTES = 65536
CHECKSUM_LINE_RE = re.compile(r"^([0-9a-f]{64}) [ \*](?:\./)?([A-Za-z0-9._-]+)$")
METADATA_FILES = ("release-manifest.json", "deployment.json", "backup-manifest.json")


def fail(message: str) -> "NoReturn":  # type: ignore[name-defined]
    raise ValueError(message)


def _dir_open_flags() -> int:
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    if hasattr(os, "O_DIRECTORY"):
        flags |= os.O_DIRECTORY
    if hasattr(os, "O_CLOEXEC"):
        flags |= os.O_CLOEXEC
    return flags


def _file_open_flags() -> int:
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    if hasattr(os, "O_NONBLOCK"):
        flags |= os.O_NONBLOCK
    if hasattr(os, "O_CLOEXEC"):
        flags |= os.O_CLOEXEC
    return flags


def open_nofollow_rdonly(path: str) -> int:
    return os.open(path, _file_open_flags())


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


class _ExactFDReader:
    def __init__(self, fd: int, expected_size: int, rel: str) -> None:
        self._fd = fd
        self._remaining = expected_size
        self._rel = rel

    def read(self, size: int = -1) -> bytes:
        if self._remaining <= 0:
            return b""
        want = self._remaining if size is None or size < 0 else min(size, self._remaining)
        chunk = os.read(self._fd, want)
        if not chunk:
            fail(f"concurrent truncation detected while archiving: {self._rel}")
        self._remaining -= len(chunk)
        return chunk


def _walk_confined_entries(root_dir: Path, _hooks: dict | None = None):
    root_stat = os.lstat(root_dir)
    if stat.S_ISLNK(root_stat.st_mode) or not stat.S_ISDIR(root_stat.st_mode):
        fail(f"sandbox root must be a real non-symlink directory: {root_dir}")

    try:
        root_fd = os.open(str(root_dir), _dir_open_flags())
    except OSError as exc:
        fail(f"sandbox root must be a real non-symlink directory: {root_dir} ({exc})")

    on_listed = _hooks.get("on_dir_listed") if _hooks else None
    on_stat = _hooks.get("on_entry_stat") if _hooks else None
    frames: list[list] = []
    try:
        root_fst = os.fstat(root_fd)
        if not stat.S_ISDIR(root_fst.st_mode) or (root_fst.st_dev, root_fst.st_ino) != (
            root_stat.st_dev,
            root_stat.st_ino,
        ):
            os.close(root_fd)
            fail(f"concurrent replacement of sandbox root detected: {root_dir}")
        root_names = sorted(os.listdir(root_fd))
        if on_listed is not None:
            on_listed("")
        frames.append(["", root_fd, root_names, 0])

        while frames:
            frame = frames[-1]
            rel_prefix, current_fd, names, idx = frame
            if idx >= len(names):
                frames.pop()
                os.close(current_fd)
                continue
            name = names[idx]
            frame[3] = idx + 1
            rel = f"{rel_prefix}/{name}" if rel_prefix else name
            validate_rel_path(rel)

            try:
                st = os.stat(name, dir_fd=current_fd, follow_symlinks=False)
            except OSError as exc:
                fail(f"concurrent modification detected while scanning {rel}: {exc}")

            if on_stat is not None:
                on_stat(rel)

            mode = st.st_mode
            if stat.S_ISLNK(mode):
                fail(f"symlink entry rejected in sandbox archive: {rel}")
            if stat.S_ISDIR(mode):
                if len(frames) >= MAX_ARCHIVE_DEPTH:
                    fail(f"sandbox archive directory depth exceeded safety bound: {rel}")
                try:
                    child_fd = os.open(name, _dir_open_flags(), dir_fd=current_fd)
                except OSError as exc:
                    fail(f"symlink or concurrent directory replacement rejected in sandbox archive: {rel} ({exc})")
                try:
                    child_fst = os.fstat(child_fd)
                    if not stat.S_ISDIR(child_fst.st_mode) or (child_fst.st_dev, child_fst.st_ino) != (
                        st.st_dev,
                        st.st_ino,
                    ):
                        fail(f"concurrent directory replacement detected while archiving: {rel}")
                    child_names = sorted(os.listdir(child_fd))
                except Exception:
                    os.close(child_fd)
                    raise
                if on_listed is not None:
                    on_listed(rel)
                frames.append([rel, child_fd, child_names, 0])
                yield rel, -1, child_fst, True
            elif stat.S_ISREG(mode):
                try:
                    file_fd = os.open(name, _file_open_flags(), dir_fd=current_fd)
                except OSError as exc:
                    fail(f"symlink or concurrent file replacement rejected in sandbox archive: {rel} ({exc})")
                try:
                    file_fst = os.fstat(file_fd)
                    if not stat.S_ISREG(file_fst.st_mode) or (file_fst.st_dev, file_fst.st_ino) != (
                        st.st_dev,
                        st.st_ino,
                    ):
                        fail(f"concurrent file replacement detected while archiving: {rel}")
                    yield rel, file_fd, file_fst, False
                finally:
                    os.close(file_fd)
            else:
                fail(f"unsupported special file rejected in sandbox archive: {rel}")
    finally:
        while frames:
            _, fd_to_close, _, _ = frames.pop()
            try:
                os.close(fd_to_close)
            except OSError:
                pass


def write_tar_gz_stream(
    source_dir: Path,
    out_stream,
    allow_missing: bool = False,
    _hooks: dict | None = None,
) -> dict:
    try:
        os.lstat(source_dir)
    except FileNotFoundError:
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
            for rel, file_fd, st, is_dir in _walk_confined_entries(source_dir, _hooks=_hooks):
                entry_count += 1
                if entry_count > MAX_ARCHIVE_ENTRIES:
                    fail("sandbox archive entry count exceeded safety bound")
                info = tarfile.TarInfo(name=rel)
                info.uid = int(st.st_uid)
                info.gid = int(st.st_gid)
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

                reader = _ExactFDReader(file_fd, st.st_size, rel)
                tf.addfile(info, fileobj=reader)
                after_fst = os.fstat(file_fd)
                if after_fst.st_size != st.st_size:
                    fail(f"concurrent modification detected while archiving: {rel}")
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


def _resolve_restore_scope(scope: str, archive_path: Path, target_dir: Path) -> str:
    if scope not in VALID_RESTORE_SCOPES:
        fail(f"invalid restore scope {scope!r}; expected one of {VALID_RESTORE_SCOPES}")
    if scope != "auto":
        return scope
    arch_name = archive_path.name.lower()
    tgt_name = target_dir.name.lower()
    if "workspace" in arch_name or "workspace" in tgt_name:
        return "workspaces"
    return "state"


def _desired_restore_owner(
    effective_scope: str,
    parts: tuple[str, ...],
    is_dir: bool,
    safe_mode: int,
    member_uid: int,
    member_gid: int,
) -> tuple[int, int]:
    if effective_scope != "workspaces":
        return (0, 0)
    # Top-level <agent-hash> directory under sandbox-workspaces stays 0:0 so supervisor
    # WorkspaceDir chmod(0777) succeeds under cap_drop=[ALL].
    if len(parts) <= 1:
        return (0, 0)
    # Supervisor-created shared directories (0777) and shared files (0666) stay 0:0.
    if member_uid == 0 and member_gid == 0:
        if (is_dir and safe_mode == 0o777) or (not is_dir and safe_mode == 0o666):
            return (0, 0)
    return (SANDBOX_RUNNER_UID, SANDBOX_RUNNER_GID)


def _ensure_restore_dir_path(
    root_fd: int,
    parts: tuple[str, ...],
    effective_scope: str,
    dir_specs: dict[tuple[str, ...], tuple[int, int, int, int | None]],
) -> int:
    cur_fd = root_fd
    try:
        for idx, part in enumerate(parts):
            prefix = parts[: idx + 1]
            try:
                os.mkdir(part, 0o700, dir_fd=cur_fd)
            except FileExistsError:
                pass
            except OSError as exc:
                fail(f"unable to create restore directory {'/'.join(prefix)}: {exc}")

            try:
                next_fd = os.open(part, _dir_open_flags(), dir_fd=cur_fd)
            except OSError as exc:
                fail(f"non-directory or symlink parent encountered during restore: {'/'.join(prefix)} ({exc})")

            fst = os.fstat(next_fd)
            if not stat.S_ISDIR(fst.st_mode):
                os.close(next_fd)
                fail(f"non-directory parent encountered during restore: {'/'.join(prefix)}")

            if prefix not in dir_specs:
                imp_mode = 0o777 if (effective_scope == "workspaces" and len(prefix) == 1) else 0o755
                imp_uid, imp_gid = _desired_restore_owner(
                    effective_scope,
                    prefix,
                    True,
                    imp_mode,
                    SANDBOX_RUNNER_UID,
                    SANDBOX_RUNNER_GID,
                )
                dir_specs[prefix] = (imp_uid, imp_gid, imp_mode, None)

            if cur_fd != root_fd:
                os.close(cur_fd)
            cur_fd = next_fd
        return cur_fd
    except Exception:
        if cur_fd != root_fd:
            try:
                os.close(cur_fd)
            except OSError:
                pass
        raise


def _open_existing_subdir(root_fd: int, parts: tuple[str, ...]) -> int:
    cur_fd = root_fd
    try:
        for idx, part in enumerate(parts):
            try:
                next_fd = os.open(part, _dir_open_flags(), dir_fd=cur_fd)
            except OSError as exc:
                fail(f"unable to reopen restored directory {'/'.join(parts[: idx + 1])}: {exc}")
            if cur_fd != root_fd:
                os.close(cur_fd)
            cur_fd = next_fd
        return cur_fd
    except Exception:
        if cur_fd != root_fd:
            try:
                os.close(cur_fd)
            except OSError:
                pass
        raise


def _write_all(fd: int, data: bytes) -> None:
    view = memoryview(data)
    offset = 0
    while offset < len(view):
        written = os.write(fd, view[offset:])
        if written <= 0:
            fail("short write to file descriptor during restore")
        offset += written


def restore_archive(
    archive_path: Path,
    target_dir: Path,
    root_mode: int = 0o700,
    max_bytes: int = DEFAULT_MAX_ARCHIVE_BYTES,
    scope: str = "auto",
    chown_fn=None,
) -> dict:
    effective_scope = _resolve_restore_scope(scope, archive_path, target_dir)
    summary = inspect_and_validate_archive(archive_path, max_bytes=max_bytes)

    if chown_fn is None and hasattr(os, "geteuid") and os.geteuid() == 0 and hasattr(os, "fchown"):
        chown_fn = os.fchown

    if target_dir.exists() or target_dir.is_symlink():
        st = os.lstat(target_dir)
        if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
            fail(f"restore target must be a real directory: {target_dir}")
    else:
        target_dir.mkdir(parents=True, exist_ok=False, mode=0o700)

    try:
        root_fd = os.open(str(target_dir), _dir_open_flags())
    except OSError as exc:
        fail(f"restore target must be a real directory: {target_dir} ({exc})")

    dir_specs: dict[tuple[str, ...], tuple[int, int, int, int | None]] = {}
    try:
        root_fst = os.fstat(root_fd)
        if not stat.S_ISDIR(root_fst.st_mode):
            fail(f"restore target must be a real directory: {target_dir}")
        if os.listdir(root_fd):
            fail(f"refusing to restore into non-empty target directory: {target_dir}")
        os.fchmod(root_fd, 0o700)

        with tarfile.open(archive_path, mode="r:gz") as tf:
            for member in tf:
                rel = validate_rel_path(member.name)
                if not rel:
                    continue
                parts = tuple(rel.split("/"))
                if len(parts) > MAX_ARCHIVE_DEPTH:
                    fail(f"archive member exceeds maximum directory depth: {rel}")

                safe_mode = member.mode & 0o777
                if member.isdir():
                    dfd = _ensure_restore_dir_path(root_fd, parts, effective_scope, dir_specs)
                    if dfd != root_fd:
                        os.close(dfd)
                    desired_mode = safe_mode if safe_mode else 0o700
                    uid, gid = _desired_restore_owner(
                        effective_scope,
                        parts,
                        True,
                        desired_mode,
                        int(member.uid),
                        int(member.gid),
                    )
                    dir_specs[parts] = (uid, gid, desired_mode, int(member.mtime))
                    continue

                parent_parts, leaf = parts[:-1], parts[-1]
                parent_fd = _ensure_restore_dir_path(root_fd, parent_parts, effective_scope, dir_specs)
                try:
                    open_flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
                    if hasattr(os, "O_NOFOLLOW"):
                        open_flags |= os.O_NOFOLLOW
                    if hasattr(os, "O_CLOEXEC"):
                        open_flags |= os.O_CLOEXEC
                    file_mode = safe_mode if safe_mode else 0o600
                    uid, gid = _desired_restore_owner(
                        effective_scope,
                        parts,
                        False,
                        file_mode,
                        int(member.uid),
                        int(member.gid),
                    )
                    fd = os.open(leaf, open_flags, 0o600, dir_fd=parent_fd)
                    try:
                        reader = tf.extractfile(member)
                        if reader is None:
                            fail(f"missing data stream for {rel}")
                        written = 0
                        while True:
                            chunk = reader.read(CHUNK_BYTES)
                            if not chunk:
                                break
                            _write_all(fd, chunk)
                            written += len(chunk)
                        if written != member.size:
                            fail(f"short write while restoring {rel}")
                        os.fsync(fd)
                        os.utime(fd, (member.mtime, member.mtime))
                        if chown_fn is not None:
                            chown_fn(fd, uid, gid)
                        os.fchmod(fd, file_mode)
                    finally:
                        os.close(fd)
                finally:
                    if parent_fd != root_fd:
                        os.close(parent_fd)

        for parts, (uid, gid, desired_mode, mtime) in sorted(
            dir_specs.items(), key=lambda item: len(item[0]), reverse=True
        ):
            dfd = _open_existing_subdir(root_fd, parts)
            try:
                if mtime is not None:
                    os.utime(dfd, (mtime, mtime))
                if chown_fn is not None:
                    chown_fn(dfd, uid, gid)
                os.fchmod(dfd, desired_mode)
            finally:
                if dfd != root_fd:
                    os.close(dfd)

        if chown_fn is not None:
            chown_fn(root_fd, 0, 0)
        os.fchmod(root_fd, root_mode & 0o777)
    finally:
        os.close(root_fd)

    summary["scope"] = effective_scope
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
    restore_cmd.add_argument("--scope", choices=VALID_RESTORE_SCOPES, default="auto")
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
                        scope=args.scope,
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
