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
	"log"
	"os"
	"path/filepath"
	"syscall"
)

// PrepareOutput checks only the paths owned by this action. Callers guarantee one writer.
func PrepareOutput(directory string, names ...string) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return OutputError(directory, err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return OutputError(directory, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("output %q must be a real directory", directory)
	}
	for _, name := range names {
		path := filepath.Join(directory, name)
		if _, err := os.Lstat(path); err == nil {
			return OutputError(path, os.ErrExist)
		} else if !os.IsNotExist(err) {
			return OutputError(path, err)
		}
	}
	return nil
}

func OutputError(name string, err error) error {
	if err == nil {
		return nil
	}
	if os.IsExist(err) {
		return Tag(DataConflict, fmt.Errorf("output %q already exists (earlier output, incomplete work, or unsupported concurrent writer); use one preparer per output location; reuse a successful artifact downstream, explicitly clean conflicting items, or choose another location: %w", name, err))
	}
	return fmt.Errorf("output %q: %w", name, err)
}

// Publish links a complete, synced temporary without replacing an existing result.
func Publish(temporary, destination string) error {
	if err := os.Link(temporary, destination); err != nil {
		return OutputError(destination, err)
	}
	if err := SyncDir(filepath.Dir(destination)); err != nil {
		return OutputError(destination, errors.Join(err, os.Remove(destination)))
	}
	if err := os.Remove(temporary); err != nil {
		log.Printf("published %q; temporary cleanup %q failed: %v", destination, temporary, err)
	}
	return nil
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
		return OutputError(name, err)
	}
	defer func() { err = OutputError(name, errors.Join(err, f.Close())) }()
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
