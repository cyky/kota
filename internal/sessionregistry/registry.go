// Package sessionregistry coordinates host processes that use a project sandbox.
// Locks live outside the sandbox and are released by the OS when a process dies.
package sessionregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Session describes a running or starting host session. Model is empty until
// model discovery completes, and stays empty for shell, prep and check.
type Session struct {
	Project string
	Agent   string
	Model   string
	PID     int
	Started time.Time
}

// Busy names the session already using a project.
type Busy struct{ Session Session }

func (b *Busy) Error() string {
	return fmt.Sprintf("project %s is already in use by %s (PID %d, started %s)",
		b.Session.Project, b.Session.Agent, b.Session.PID, b.Session.Started.Format(time.RFC3339))
}

// Registry stores one persistent lock file per resolved project path. Never
// delete these files: replacing a locked inode would allow a second owner.
type Registry struct {
	Dir string
	// Recover stops orphaned containers before replacing a crashed session.
	// Failure leaves its record intact and refuses the new session.
	Recover func(project string) error
}

// Lease owns a project until Close, including startup and teardown.
type Lease struct {
	registry *Registry
	file     *os.File
	caches   *os.File
	session  Session
	mu       sync.Mutex
}

// CachesBusy reports that a shared-cache reset conflicts with other sessions.
type CachesBusy struct{ Exclusive bool }

func (b *CachesBusy) Error() string {
	if b.Exclusive {
		return "shared caches are in use by another session; wait for all sessions to finish before cleaning shared caches"
	}
	return "shared caches are being cleaned; retry when cleanup finishes"
}

// Acquire atomically refuses an existing owner or locks the project. Paths
// must already be resolved and validated by the caller. A free OS lock with
// a nonempty record means the previous owner crashed, regardless of PID reuse.
func (r *Registry) Acquire(project, agent, model string) (*Lease, error) {
	return r.acquire(project, agent, model, false)
}

// AcquireExclusive reserves the shared caches as well as the project. Used
// when cleaning shared caches; refuses while any other session is starting,
// running or tearing down, including gaps between prep's Docker containers.
func (r *Registry) AcquireExclusive(project, agent, model string) (*Lease, error) {
	return r.acquire(project, agent, model, true)
}

func (r *Registry) acquire(project, agent, model string, exclusive bool) (*Lease, error) {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return nil, err
	}
	caches, err := os.OpenFile(filepath.Join(r.Dir, ".caches"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			caches.Close()
		}
	}()
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(caches.Fd()), mode|syscall.LOCK_NB); err != nil {
		if wouldBlock(err) {
			return nil, &CachesBusy{Exclusive: exclusive}
		}
		return nil, err
	}
	guard, err := r.guard()
	if err != nil {
		return nil, err
	}
	defer func() {
		if guard != nil {
			guard.Close()
		}
	}()
	f, err := os.OpenFile(r.filename(project), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !keep {
			f.Close()
		}
	}()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !wouldBlock(err) {
			return nil, err
		}
		s, err := read(f)
		if err != nil {
			return nil, fmt.Errorf("read busy session: %w", err)
		}
		return nil, &Busy{Session: s}
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 0 && r.Recover != nil {
		// The project flock alone protects recovery. Docker can stall, so
		// release the metadata guard while keeping this project's lease.
		// Leave the old record intact until recovery succeeds.
		guard.Close()
		guard = nil
		if err := r.Recover(project); err != nil {
			return nil, fmt.Errorf("recover crashed session for %s: %w", project, err)
		}
		guard, err = r.guard()
		if err != nil {
			return nil, err
		}
	}
	s := Session{Project: project, Agent: agent, Model: model, PID: os.Getpid(), Started: time.Now().UTC()}
	if err := write(f, s); err != nil {
		return nil, err
	}
	keep = true
	return &Lease{registry: r, file: f, caches: caches, session: s}, nil
}

// Active returns a consistent snapshot of sessions with an OS lock still held.
// Stale records are retained for recovery by the next Acquire of that project.
func (r *Registry) Active() ([]Session, error) {
	guard, err := r.guard()
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	return r.active()
}

func (r *Registry) active() ([]Session, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		f, err := os.OpenFile(filepath.Join(r.Dir, entry.Name()), os.O_RDWR, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			f.Close()
			continue
		}
		if !wouldBlock(lockErr) {
			f.Close()
			return nil, lockErr
		}
		s, err := read(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("read active session: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// SetModel records the resolved model and returns other active sessions using
// different models. Update and comparison share the registry guard so two
// simultaneous starts cannot both miss the clash.
func (l *Lease) SetModel(model string) ([]Session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil, os.ErrClosed
	}
	guard, err := l.registry.guard()
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	l.session.Model = model
	if err := write(l.file, l.session); err != nil {
		return nil, err
	}
	active, err := l.registry.active()
	if err != nil {
		return nil, err
	}
	var clashes []Session
	for _, other := range active {
		if other.Project != l.session.Project && other.Model != "" && model != "" && other.Model != model {
			clashes = append(clashes, other)
		}
	}
	return clashes, nil
}

// Close clears the record and releases the lock. Repeated calls are harmless.
func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	guard, err := l.registry.guard()
	if err != nil {
		// Still release the OS lock; leave the record for recovery.
		fileErr := l.file.Close()
		cacheErr := l.caches.Close()
		l.file = nil
		return errors.Join(err, fileErr, cacheErr)
	}
	defer guard.Close()
	err = l.file.Truncate(0)
	closeErr := l.file.Close()
	cacheErr := l.caches.Close()
	l.file = nil
	return errors.Join(err, closeErr, cacheErr)
}

func (r *Registry) filename(project string) string {
	sum := sha256.Sum256([]byte(project))
	return filepath.Join(r.Dir, hex.EncodeToString(sum[:])+".lock")
}

// The short-lived guard serializes metadata reads and writes. Each lease has
// its own lock, so the guard is never held while an agent or shell runs.
func (r *Registry) guard() (*os.File, error) {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(r.Dir, ".guard"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func wouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func read(f *os.File) (Session, error) {
	var s Session
	_, err := f.Seek(0, io.SeekStart)
	if err == nil {
		err = json.NewDecoder(f).Decode(&s)
	}
	return s, err
}

func write(f *os.File, s Session) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	return json.NewEncoder(f).Encode(s)
}
