//go:build !linux

package fspath

// FileID returns the device and inode numbers of the file name, after its symbolic links, as stat(2) tells them. ok is
// false on a system other than Linux, where FileID does not look.
func FileID(string) (dev, ino uint64, ok bool, err error) { return 0, 0, false, nil }
