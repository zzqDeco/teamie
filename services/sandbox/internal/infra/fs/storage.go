// Package fs implements the local filesystem and receipt persistence helpers
// for the Sandbox bounded context.
package fs

import (
	"os"

	"teamie/services/sandbox/internal/domain"
)

type WorkspaceDirectories = domain.WorkspaceDirectories

func WriteAtomicJSON(target string, r any) error {
	return domain.WriteAtomicJSON(target, r)
}

func RegularRead(root *os.Root, name string) ([]byte, error) {
	return domain.RegularRead(root, name)
}

func ReadPageFile(root *os.Root, name string) ([]byte, error) {
	return domain.ReadPageFile(root, name)
}

func OpenWorkspaceDirectory(root *os.Root, name string) (*os.File, error) {
	return domain.OpenWorkspaceDirectory(root, name)
}

func EnsureWorkspaceParents(root *os.Root, parent string) error {
	return domain.EnsureWorkspaceParents(root, parent)
}

func AtomicWorkspaceWrite(root *os.Root, name, content string) error {
	return domain.AtomicWorkspaceWrite(root, name, content)
}

func WorkspaceCapacity(root *os.Root, name string, replacementBytes int64) bool {
	return domain.WorkspaceCapacity(root, name, replacementBytes)
}

func WorkspaceWithinQuota(workspace string) bool {
	return domain.WorkspaceWithinQuota(workspace)
}

func BinaryHash(root *os.Root, name string) (string, error) {
	return domain.BinaryHash(root, name)
}

func AtomicBinaryImport(root *os.Root, name string, content []byte) error {
	return domain.AtomicBinaryImport(root, name, content)
}
