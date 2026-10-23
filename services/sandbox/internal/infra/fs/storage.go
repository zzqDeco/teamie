// Package fs implements the local filesystem and receipt persistence outbound
// port (domain.WorkspaceStorage) for the Sandbox bounded context.
package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"teamie/services/sandbox/internal/domain"
)

var _ domain.WorkspaceStorage = Storage{}

func init() {
	domain.DefaultWorkspaceStorage = Storage{}
}

// Storage implements domain.WorkspaceStorage using confined OS root handles and atomic renames.
type Storage struct{}

// NewStorage returns a new filesystem Storage adapter.
func NewStorage() Storage {
	return Storage{}
}

func (Storage) WriteAtomicJSON(target string, r any) error {
	return WriteAtomicJSON(target, r)
}

func (Storage) RegularRead(root *os.Root, name string) ([]byte, error) {
	return RegularRead(root, name)
}

func (Storage) ReadPageFile(root *os.Root, name string) ([]byte, error) {
	return ReadPageFile(root, name)
}

func (Storage) OpenWorkspaceDirectory(root *os.Root, name string) (*os.File, error) {
	return OpenWorkspaceDirectory(root, name)
}

func (Storage) EnsureWorkspaceParents(root *os.Root, parent string) error {
	return EnsureWorkspaceParents(root, parent)
}

func (Storage) AtomicWorkspaceWrite(root *os.Root, name, content string) error {
	return AtomicWorkspaceWrite(root, name, content)
}

func (Storage) WorkspaceCapacity(root *os.Root, name string, replacementBytes int64) bool {
	return WorkspaceCapacity(root, name, replacementBytes)
}

func (Storage) WorkspaceWithinQuota(workspace string) bool {
	return WorkspaceWithinQuota(workspace)
}

func (Storage) BinaryHash(root *os.Root, name string) (string, error) {
	return BinaryHash(root, name)
}

func (Storage) AtomicBinaryImport(root *os.Root, name string, content []byte) error {
	return AtomicBinaryImport(root, name, content)
}

func (Storage) AcquireSupervisorLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another sandbox supervisor owns this state")
	}
	return lock, nil
}

func (Storage) ReleaseSupervisorLock(f *os.File) {
	if f != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

func (Storage) DiskAvailable(p string) bool {
	var stat syscall.Statfs_t
	return syscall.Statfs(p, &stat) == nil && stat.Bavail > 0 && uint64(stat.Bavail)*uint64(stat.Bsize) >= 2*1024*1024*1024
}

func (Storage) OpenNonblock(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func (Storage) OpenNonblockNoFollow(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

func (Storage) OpenNoFollowFile(p string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(p, flag|syscall.O_NOFOLLOW, perm)
}

// WriteAtomicJSON writes r as JSON to a temporary file in filepath.Dir(target),
// fsyncs the file, renames it over target, and fsyncs the parent directory.
func WriteAtomicJSON(target string, r any) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".receipt-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, target)
	}
	if err == nil {
		d, openErr := os.Open(filepath.Dir(target))
		if openErr != nil {
			return openErr
		}
		err = d.Sync()
		d.Close()
	}
	return err
}

// RegularRead reads a regular UTF-8 text file up to MaxFileBytes from root.
func RegularRead(root *os.Root, name string) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > domain.MaxFileBytes {
		return nil, errors.New("file_too_large")
	}
	b, err := io.ReadAll(io.LimitReader(f, domain.MaxFileBytes+1))
	if len(b) > domain.MaxFileBytes {
		return nil, errors.New("file_too_large")
	}
	if !utf8.Valid(b) {
		return nil, errors.New("binary file")
	}
	return b, err
}

// ReadPageFile reads a regular UTF-8 text file up to MaxReadableFileBytes from root.
func ReadPageFile(root *os.Root, name string) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() {
		return nil, errors.New("file_unavailable")
	}
	if i.Size() > domain.MaxReadableFileBytes {
		return nil, errors.New("file_too_large")
	}
	b, err := io.ReadAll(io.LimitReader(f, domain.MaxReadableFileBytes+1))
	if len(b) > domain.MaxReadableFileBytes {
		return nil, errors.New("file_too_large")
	}
	if !utf8.Valid(b) {
		return nil, errors.New("binary_file")
	}
	return b, err
}

// OpenWorkspaceDirectory opens a confined non-symlink directory with O_DIRECTORY|O_NOFOLLOW|O_NONBLOCK.
func OpenWorkspaceDirectory(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("not a directory")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() || !os.SameFile(before, info) {
		f.Close()
		if err == nil {
			err = errors.New("not a directory")
		}
		return nil, err
	}
	return f, nil
}

// EnsureWorkspaceParents creates and chmods all parent directories inside root.
func EnsureWorkspaceParents(root *os.Root, parent string) error {
	current := ""
	if parent == "." {
		return nil
	}
	for _, part := range strings.Split(parent, "/") {
		current = path.Join(current, part)
		if err := root.Mkdir(current, 0777); err != nil && !os.IsExist(err) {
			return err
		}
		f, err := OpenWorkspaceDirectory(root, current)
		if err != nil {
			return err
		}
		err = f.Chmod(0777)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// AtomicWorkspaceWrite atomically writes content to name inside root.
func AtomicWorkspaceWrite(root *os.Root, name, content string) error {
	if name == "." || len(content) > domain.MaxFileBytes {
		return errors.New("file_too_large")
	}
	parent := path.Dir(name)
	if err := EnsureWorkspaceParents(root, parent); err != nil {
		return err
	}
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	nameTmp := path.Join(parent, ".teamie-write-"+domain.Digest(time.Now().UTC().String()+name)[:24])
	f, err := root.OpenFile(nameTmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0666)
	if err != nil {
		return err
	}
	defer root.Remove(nameTmp)
	if err = f.Chmod(0666); err == nil {
		_, err = io.WriteString(f, content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(nameTmp, name)
	}
	if err == nil {
		dir, openErr := OpenWorkspaceDirectory(root, parent)
		if openErr != nil {
			return openErr
		}
		err = dir.Sync()
		dir.Close()
	}
	return err
}

// WorkspaceDirectories wraps *os.Root so io/fs.WalkDir uses nonblocking directory opens.
type WorkspaceDirectories struct {
	Root *os.Root
}

func (d WorkspaceDirectories) Open(name string) (iofs.File, error) {
	return OpenWorkspaceDirectory(d.Root, name)
}

// WorkspaceCapacity checks whether writing replacementBytes to name stays within workspace file/byte limits.
func WorkspaceCapacity(root *os.Root, name string, replacementBytes int64) bool {
	var total int64
	entries := 0
	err := iofs.WalkDir(WorkspaceDirectories{Root: root}, ".", func(current string, entry iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > domain.MaxWorkspaceFiles {
			return errors.New("workspace_capacity")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && current != name {
			total += info.Size()
		}
		if total+replacementBytes > domain.MaxWorkspaceBytes {
			return errors.New("workspace_capacity")
		}
		return nil
	})
	return err == nil && total+replacementBytes <= domain.MaxWorkspaceBytes && entries < domain.MaxWorkspaceFiles
}

// WorkspaceWithinQuota reports whether workspace is within its file/byte quota.
func WorkspaceWithinQuota(workspace string) bool {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return false
	}
	defer root.Close()
	return WorkspaceCapacity(root, "", 0)
}

// BinaryHash computes the SHA-256 hex digest of a regular attachment file inside root.
func BinaryHash(root *os.Root, name string) (string, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > domain.AttachmentBytes {
		return "", errors.New("invalid file")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, domain.AttachmentBytes+1))
	if err != nil || n > domain.AttachmentBytes {
		return "", errors.New("invalid file")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AtomicBinaryImport atomically publishes content to name inside root without overwriting existing files.
func AtomicBinaryImport(root *os.Root, name string, content []byte) error {
	parent := path.Dir(name)
	if err := EnsureWorkspaceParents(root, parent); err != nil {
		return err
	}
	tmp := path.Join(parent, ".teamie-import-"+domain.Digest(time.Now().UTC().String()+name)[:24])
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0666)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if err = f.Chmod(0666); err == nil {
		_, err = f.Write(content)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = root.Link(tmp, name); err != nil {
		return err
	}
	if err = root.Remove(tmp); err != nil {
		return err
	}
	dir, err := OpenWorkspaceDirectory(root, parent)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	return err
}
