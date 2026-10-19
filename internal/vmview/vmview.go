// Package vmview waits until the Docker VM sees project paths as the host
// does.
//
// On a virtiofs engine (Rancher Desktop with VZ), the VM caches file
// lookups in the shared project for about a second. A file replaced or
// deleted on the host within that window is still the old one in the VM,
// and a mount over it is dropped, together with the stale entry, when the
// entry expires. Before a session mounts over project paths, Wait polls
// the VM's view of them until it matches the host's.
package vmview

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Interval is how often the VM's view is read.
const Interval = 250 * time.Millisecond

// Script runs in a container that has the project mounted read-only, with
// it as the working directory. Its arguments are the interval, then each
// path with the state the host has it in (see state). Every interval it
// compares each path's state in the VM with the host's, and prints "ok"
// and exits when all match; otherwise it prints "differ", then index=state
// for each path that differs. An mtime without nanoseconds compares to the
// second.
const Script = `# vmview: compare the Docker VM's view of project paths with the host's
trap 'exit 143' TERM
interval=$1; shift
n=$(($# / 2))
while :; do
  out=differ i=0
  while [ "$i" -lt "$n" ]; do
    p=$1 want=$2; shift 2; set -- "$@" "$p" "$want"
    if [ -L "$p" ]; then got=other
    elif [ -d "$p" ]; then got=dir
    elif [ -f "$p" ]; then got=$(stat -c 'file|%s|%.9Y' -- "$p" 2>/dev/null) || got=missing
    elif [ -e "$p" ]; then got=other
    else got=missing
    fi
    case $got in
      "$want") ;;
      *.000000000) [ "${got%.*}" = "${want%.*}" ] || out="$out $i=$got" ;;
      *) out="$out $i=$got" ;;
    esac
    i=$((i + 1))
  done
  if [ "$out" = differ ]; then echo ok; exit 0; fi
  echo "$out"
  sleep "$interval"
done`

// Start runs Script in such a container with args. It returns the
// script's stdout, and stop, which ends the container if it still runs and
// waits for it.
type Start func(args []string) (stdout io.Reader, stop func() error, err error)

// Wait waits until the VM sees each of paths (relative to project, with
// forward slashes) as the host does, for at most timeout. Directories
// compare by type, files by size and mtime too. If they don't match in
// time, the error names the paths that differ.
func Wait(project string, paths []string, timeout time.Duration, start Start) error {
	if len(paths) == 0 {
		return nil
	}
	args := []string{strconv.FormatFloat(Interval.Seconds(), 'f', -1, 64)}
	host := make([]string, len(paths))
	for i, p := range paths {
		host[i] = state(filepath.Join(project, filepath.FromSlash(p)))
		args = append(args, "./"+p, host[i])
	}
	out, stop, err := start(args)
	if err != nil {
		return fmt.Errorf("compare the Docker VM's view of the project: %w", err)
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	// finish keeps reading what is left, so the container isn't blocked on
	// its output while stop waits for it.
	finish := func() error {
		go func() {
			for range lines {
			}
		}()
		return stop()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	last := ""
	for {
		select {
		case line, ok := <-lines:
			switch {
			case line == "ok":
				return finish()
			case !ok:
				err := finish()
				if err == nil {
					err = errors.New("ended without a result")
				}
				return fmt.Errorf("compare the Docker VM's view of the project: %w", err)
			}
			last = line
		case <-timer.C:
			finish()
			return refusal(paths, host, last, timeout)
		}
	}
}

// refusal names the paths that still differed in the last line.
func refusal(paths, host []string, last string, timeout time.Duration) error {
	var differ []string
	for _, f := range strings.Fields(strings.TrimPrefix(last, "differ")) {
		i, got, ok := strings.Cut(f, "=")
		n, err := strconv.Atoi(i)
		if !ok || err != nil || n < 0 || n >= len(paths) {
			continue
		}
		differ = append(differ, fmt.Sprintf("%s (host: %s; Docker VM: %s)", paths[n], describe(host[n]), describe(got)))
	}
	if len(differ) == 0 {
		return fmt.Errorf("the Docker VM's view of the project didn't match the host's within %s", timeout)
	}
	return fmt.Errorf("after %s the Docker VM still sees %d protected or masked path(s) differently from the host, so their mounts might not hold: %s",
		timeout, len(differ), strings.Join(differ, ", "))
}

// state is how Script prints path's state: missing, dir, other, or
// file|size|mtime with the mtime in seconds and nanoseconds.
func state(path string) string {
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return "missing"
	case info.Mode()&fs.ModeSymlink != 0:
		return "other"
	case info.IsDir():
		return "dir"
	case !info.Mode().IsRegular():
		return "other"
	}
	t := info.ModTime()
	return fmt.Sprintf("file|%d|%d.%09d", info.Size(), t.Unix(), t.Nanosecond())
}

// describe makes a state readable.
func describe(s string) string {
	f := strings.Split(s, "|")
	if len(f) != 3 || f[0] != "file" {
		switch s {
		case "dir":
			return "a directory"
		case "other":
			return "not a file or directory"
		}
		return s
	}
	desc := "a file of " + f[1] + " bytes"
	if sec, nsec, ok := strings.Cut(f[2], "."); ok {
		if n, err := strconv.ParseInt(sec, 10, 64); err == nil {
			ns, _ := strconv.ParseInt(nsec, 10, 64)
			desc += ", modified " + time.Unix(n, ns).UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
	}
	return desc
}
