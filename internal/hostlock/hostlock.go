// Package hostlock locks project files on the host for the length of a
// Session, with the user-immutable flag (chflags uchg on macOS).
//
// A read-only mount or a mask holds only while the file under it stays the
// same file on the host: replacing it there (a rename over it, or a delete
// and re-create) drops the mount in the Sandbox. A locked file can't be
// replaced, removed or written, so the mounts over it hold.
//
// Every file is recorded in a lock list before its flag is set, so a crash
// never leaves a lock that isn't recorded. Unlock clears the flags of the
// listed files and removes the list. A file that was locked already is
// left out, so a lock the user set is never cleared.
package hostlock

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Lock locks each file in rels (relative to project, slash-separated) that
// is a regular file inside project, with no symlink on the way, and isn't
// locked already. All of them are recorded in list, with one sync, before
// any flag is set. It returns how many it locked, and one error in failed
// for each file it couldn't lock (on a filesystem without file flags, or
// a file owned by another user); the others are locked anyway. list must
// not exist. On an error, nothing is locked.
func Lock(project string, rels []string, list string) (n int, failed []error, err error) {
	if !supported {
		return 0, nil, nil
	}
	if !filepath.IsAbs(project) || filepath.Clean(project) != project {
		return 0, nil, fmt.Errorf("lock files: project %q is not a clean absolute path", project)
	}
	type target struct {
		rel, path string
		info      fs.FileInfo
	}
	var targets []target
	entries := []string{project}
	for _, rel := range rels {
		path, info, ok := regular(project, rel)
		if !ok || locked(info) {
			continue
		}
		targets = append(targets, target{rel, path, info})
		entries = append(entries, rel)
	}
	f, err := os.OpenFile(list, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return 0, nil, fmt.Errorf("lock files: %w", err)
	}
	defer f.Close()
	if err := record(f, entries); err != nil {
		return 0, nil, err
	}
	for _, t := range targets {
		if err := setLocked(t.path, t.info, true); err != nil {
			if cause := errors.Unwrap(err); cause != nil {
				err = cause // the path is in rel
			}
			failed = append(failed, fmt.Errorf("%s: %w", t.rel, err))
			continue
		}
		n++
	}
	return n, failed, nil
}

// Unlock clears the flags of the files recorded in list, then removes it.
// A missing list is nothing to do. Entries that aren't a regular file
// inside the recorded project, with no symlink on the way, are skipped. If
// a file can't be unlocked, the list is kept and the error names it.
func Unlock(list string) error {
	data, err := os.ReadFile(list)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("unlock files: %w", err)
	}
	var project string
	var errs []error
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(nil, 1<<20)
	for first := true; sc.Scan(); first = false {
		var entry string
		if json.Unmarshal(sc.Bytes(), &entry) != nil {
			continue // cut short by a crash, or edited
		}
		if first {
			project = entry
			continue
		}
		if !filepath.IsAbs(project) || filepath.Clean(project) != project {
			return fmt.Errorf("unlock files: %s doesn't name a clean absolute project path; clear the files' locks with chflags nouchg, then remove it", list)
		}
		path, info, ok := regular(project, entry)
		if !ok || !locked(info) {
			continue
		}
		if err := setLocked(path, info, false); err != nil {
			errs = append(errs, fmt.Errorf("unlock %s: %w; clear it with chflags nouchg, then remove %s", path, err, list))
		}
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Errorf("unlock files: read %s: %w", list, err))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return os.Remove(list)
}

// record appends entries to the list, each as a JSON string on its own
// line, and syncs it once, so they're on disk before any flag is set.
func record(f *os.File, entries []string) error {
	var buf []byte
	for _, s := range entries {
		b, err := json.Marshal(s)
		if err != nil {
			return err
		}
		buf = append(append(buf, b...), '\n')
	}
	if _, err := f.Write(buf); err != nil {
		return fmt.Errorf("record lock: %w", err)
	}
	return f.Sync()
}

// regular returns the path of rel in project and its info, if rel is a
// clean relative path to a regular file there, with no symlink on the way.
func regular(project, rel string) (string, fs.FileInfo, bool) {
	if !filepath.IsLocal(filepath.FromSlash(rel)) || filepath.ToSlash(filepath.Clean(rel)) != rel {
		return "", nil, false
	}
	path := project
	var info fs.FileInfo
	for _, part := range strings.Split(rel, "/") {
		path = filepath.Join(path, part)
		var err error
		if info, err = os.Lstat(path); err != nil || info.Mode()&fs.ModeSymlink != 0 {
			return "", nil, false
		}
	}
	if !info.Mode().IsRegular() {
		return "", nil, false
	}
	return path, info, true
}
