/*
 *     Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package contract

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type Lock struct{ file *os.File }

func AcquireLock(directory, name string) (*Lock, error) {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory must be a real directory: %s", directory)
	}
	fd, err := syscall.Open(filepath.Join(directory, name), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("another writer holds %s: %w", directory, err), file.Close())
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Close() error {
	return errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close())
}

func SyncDir(directory string) error {
	f, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// WriteFile retains a failed partial file so the next invocation cannot silently
// adopt it. Callers publish only after this function and all validation succeed.
func WriteFile(name string, data []byte) (err error) {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0444)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func RequireSpace(directory string, bytes uint64) error {
	var info syscall.Statfs_t
	if err := syscall.Statfs(directory, &info); err != nil {
		return err
	}
	available := info.Bavail * uint64(info.Bsize)
	if bytes > available {
		return fmt.Errorf("insufficient free space at %s: need %d bytes, available %d", directory, bytes, available)
	}
	return nil
}
