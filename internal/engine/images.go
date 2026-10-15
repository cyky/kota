package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// RecordFile lists a project's image archives, in its archive
	// directory.
	RecordFile = "images.json"
	// DigestsFile is where FetchScript writes each image's digest.
	DigestsFile = "digests"
)

// Archive is an Engine image downloaded by prep.
type Archive struct {
	// Ref is the image as declared, and the name it gets in the Engine VM.
	Ref string `json:"ref"`
	// Digest is what Ref resolved to at prep; the archive was pulled by it.
	Digest string `json:"digest"`
	// File is the archive's name in the archive directory.
	File string `json:"file"`
}

// Record is a project's archives after its last prep.
type Record struct {
	Archives []Archive `json:"archives"`
}

// ArchiveFile returns the archive file name of a declared reference.
func ArchiveFile(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:])[:32] + ".tar"
}

// FetchScript returns the sh script that downloads images for platform with
// crane into the current directory. Each image is pulled by the digest its
// tag resolves to, so crane checks every manifest and layer against it, and
// "<ref> <digest>" is appended to DigestsFile. It runs only crane; no
// project code and no project files are involved.
func FetchScript(images []string, platform string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, ref := range images {
		repo, _ := SplitRef(ref)
		fmt.Fprintf(&b, "echo %s\n", shellQuote("==> engine image "+ref))
		fmt.Fprintf(&b, "d=$(crane digest %s)\n", shellQuote(ref))
		fmt.Fprintf(&b, "crane pull --platform %s %s\"@$d\" %s\n", shellQuote(platform), shellQuote(repo), shellQuote(ArchiveFile(ref)))
		fmt.Fprintf(&b, "printf '%%s %%s\\n' %s \"$d\" >> %s\n", shellQuote(ref), DigestsFile)
	}
	return b.String()
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ParseDigests reads FetchScript's DigestsFile content into the record of
// images, which must each have exactly one valid digest.
func ParseDigests(images []string, content string) (Record, error) {
	digests := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		ref, digest, ok := strings.Cut(line, " ")
		if !ok || !digestPattern.MatchString(digest) {
			return Record{}, fmt.Errorf("engine image digests: unexpected line %q", line)
		}
		if _, dup := digests[ref]; dup {
			return Record{}, fmt.Errorf("engine image digests: %s listed twice", ref)
		}
		digests[ref] = digest
	}
	var r Record
	for _, ref := range images {
		d, ok := digests[ref]
		if !ok {
			return Record{}, fmt.Errorf("engine image %s: no digest recorded", ref)
		}
		r.Archives = append(r.Archives, Archive{Ref: ref, Digest: d, File: ArchiveFile(ref)})
	}
	if len(digests) != len(images) {
		return Record{}, errors.New("engine image digests: images that weren't declared")
	}
	return r, nil
}

// LoadRecord reads the record in an archive directory. A missing record
// returns nil.
func LoadRecord(dir string) (*Record, error) {
	data, err := os.ReadFile(filepath.Join(dir, RecordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, RecordFile), err)
	}
	return &r, nil
}

// Save writes r into the archive directory dir.
func (r Record) Save(dir string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, RecordFile), data, 0o600)
}

// Select returns the archives of the declared images, in declared order,
// or an error naming the images prep hasn't downloaded.
func (r *Record) Select(images []string) ([]Archive, error) {
	byRef := map[string]Archive{}
	if r != nil {
		for _, a := range r.Archives {
			byRef[a.Ref] = a
		}
	}
	var out []Archive
	var missing []string
	for _, ref := range images {
		a, ok := byRef[ref]
		if !ok || a.File != ArchiveFile(ref) {
			missing = append(missing, ref)
			continue
		}
		out = append(out, a)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("engine images not prepped: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
