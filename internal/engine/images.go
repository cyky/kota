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
	"slices"
	"strings"
)

const (
	// RecordFile lists a project's image archives, in its archive
	// directory.
	RecordFile = "images.json"
	// DigestsFile is where ResolveScript and FetchScript write each
	// image's digest.
	DigestsFile = "digests"
	// FailedFile names the image whose lookup or download failed, when
	// ResolveScript or FetchScript stopped on one.
	FailedFile = "failed"
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

// ResolveScript returns the sh script that looks up, with crane, the digest
// each of images' tags points to now, and appends "<ref> <digest>" to
// DigestsFile in the current directory. It downloads no image.
func ResolveScript(images []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, ref := range images {
		writeResolve(&b, ref)
		writeDigest(&b, ref)
	}
	return b.String()
}

// FetchScript returns the sh script that downloads downloads for platform
// with crane into the current directory. Each image is pulled by its
// Digest, or, when that is empty, by the digest its tag resolves to now,
// so crane checks every manifest and layer against it. "<ref> <digest>" is
// appended to DigestsFile for each. It runs only crane; no project code and
// no project files are involved.
func FetchScript(downloads []Archive, platform string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, a := range downloads {
		repo, _ := SplitRef(a.Ref)
		fmt.Fprintf(&b, "echo %s\n", shellQuote("==> engine image "+a.Ref))
		if a.Digest == "" {
			writeResolve(&b, a.Ref)
		} else {
			fmt.Fprintf(&b, "d=%s\n", shellQuote(a.Digest))
		}
		fmt.Fprintf(&b, "crane pull --platform %s %s\"@$d\" %s || %s\n", shellQuote(platform), shellQuote(repo), shellQuote(ArchiveFile(a.Ref)), failed(a.Ref))
		writeDigest(&b, a.Ref)
	}
	return b.String()
}

// writeResolve sets $d to the digest ref's tag points to, or stops naming
// ref in FailedFile.
func writeResolve(b *strings.Builder, ref string) {
	fmt.Fprintf(b, "d=$(crane digest %s) || %s\n", shellQuote(ref), failed(ref))
}

func writeDigest(b *strings.Builder, ref string) {
	fmt.Fprintf(b, "printf '%%s %%s\\n' %s \"$d\" >> %s\n", shellQuote(ref), DigestsFile)
}

func failed(ref string) string {
	return fmt.Sprintf("{ printf '%%s\\n' %s > %s; exit 1; }", shellQuote(ref), FailedFile)
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ParseDigests reads DigestsFile content: each of images must have exactly
// one valid digest, and no other image may appear.
func ParseDigests(images []string, content string) (map[string]string, error) {
	digests := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		if line == "" {
			continue
		}
		ref, digest, ok := strings.Cut(line, " ")
		if !ok || !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("engine image digests: unexpected line %q", line)
		}
		if _, dup := digests[ref]; dup {
			return nil, fmt.Errorf("engine image digests: %s listed twice", ref)
		}
		digests[ref] = digest
	}
	for _, ref := range images {
		if _, ok := digests[ref]; !ok {
			return nil, fmt.Errorf("engine image %s: no digest recorded", ref)
		}
	}
	if len(digests) != len(images) {
		return nil, errors.New("engine image digests: images that weren't declared")
	}
	return digests, nil
}

// Move is a declared image whose tag points to another digest than at the
// last prep.
type Move struct {
	Ref, From, To string
}

// ImagePlan is what a prep does with a project's Engine images.
type ImagePlan struct {
	// Added are declared images the last prep didn't download. Removed
	// were downloaded, and aren't declared any more. Moved are images whose
	// tag points to another digest now.
	Added, Removed []string
	Moved          []Move
	// Downloads are the images to download: added ones, without a digest
	// until their tag is looked up after the confirmation, moved ones by
	// their new digest, and ones whose archive is gone by the recorded one.
	Downloads []Archive
	// Keep are the last prep's archives that stay as they are.
	Keep []Archive

	declared []string
}

// Changed reports whether the declared images or their digests differ from
// the last prep.
func (p ImagePlan) Changed() bool {
	return len(p.Added) > 0 || len(p.Removed) > 0 || len(p.Moved) > 0
}

// Plan compares declared with the last prep's record, nil if there is none.
// resolved maps the record's images to the digests their tags point to now;
// only images already in the record are looked up before the user confirms.
// have reports whether a recorded archive is still in place.
func Plan(declared []string, last *Record, resolved map[string]string, have func(Archive) bool) ImagePlan {
	p := ImagePlan{declared: slices.Clone(declared)}
	recorded := map[string]Archive{}
	if last != nil {
		for _, a := range last.Archives {
			recorded[a.Ref] = a
			if !slices.Contains(declared, a.Ref) {
				p.Removed = append(p.Removed, a.Ref)
			}
		}
	}
	for _, ref := range declared {
		a, ok := recorded[ref]
		if !ok {
			p.Added = append(p.Added, ref)
			p.Downloads = append(p.Downloads, Archive{Ref: ref, File: ArchiveFile(ref)})
			continue
		}
		now := resolved[ref]
		switch {
		case now == "":
			// Not looked up: download by the tag, after the confirmation.
			p.Downloads = append(p.Downloads, Archive{Ref: ref, File: ArchiveFile(ref)})
		case now != a.Digest:
			p.Moved = append(p.Moved, Move{Ref: ref, From: a.Digest, To: now})
			p.Downloads = append(p.Downloads, Archive{Ref: ref, Digest: now, File: ArchiveFile(ref)})
		case a.File != ArchiveFile(ref) || !have(a):
			p.Downloads = append(p.Downloads, Archive{Ref: ref, Digest: now, File: ArchiveFile(ref)})
		default:
			p.Keep = append(p.Keep, a)
		}
	}
	return p
}

// Record returns the record after a prep that downloaded p.Downloads, with
// the digests they were pulled by, in declared order. A download pulled by
// another digest than planned is an error.
func (p ImagePlan) Record(digests map[string]string) (Record, error) {
	kept := map[string]Archive{}
	for _, a := range p.Keep {
		kept[a.Ref] = a
	}
	planned := map[string]Archive{}
	for _, a := range p.Downloads {
		planned[a.Ref] = a
	}
	var r Record
	for _, ref := range p.declared {
		if a, ok := kept[ref]; ok {
			r.Archives = append(r.Archives, a)
			continue
		}
		a, ok := planned[ref]
		d := digests[ref]
		if !ok || !digestPattern.MatchString(d) {
			return Record{}, fmt.Errorf("engine image %s: no digest recorded", ref)
		}
		if a.Digest != "" && a.Digest != d {
			return Record{}, fmt.Errorf("engine image %s: pulled %s, planned %s", ref, d, a.Digest)
		}
		r.Archives = append(r.Archives, Archive{Ref: ref, Digest: d, File: ArchiveFile(ref)})
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
