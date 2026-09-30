package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"slices"
	"sort"

	"github.com/oarkflow/ref/platform"
)

// Bundle revisions.
//
// A revision proposed with Manager.ProposeBundle carries its files (Files) as
// well as the document derived from them (Source, the files joined exactly as
// platform.Bundle.Source does). Everything that signs or verifies a revision
// treats the two formats differently:
//
//   - A revision with no Files is a legacy revision. Its checksum is the
//     SHA-256 of Source and its payloads are the v1 strings; nothing about
//     them has changed, so revisions signed before bundles existed keep
//     verifying.
//   - A revision with Files and Assets is a v3 revision: its checksum is
//     AssetsChecksum, so the assets are signed with the files, and its payloads
//     carry "v3". Assets without Files, or Files whose checksum is the v2 one,
//     are refused.
//   - A revision with Files is a bundle revision. Its checksum is BundleChecksum,
//     its HMAC and key signatures cover v2 payloads, and Verify re-derives
//     Source from Files and compares it. A bundle can therefore not be turned
//     into a legacy revision (the checksum would not match) or the reverse.

// BundleVersion tags the payload format of bundle revisions.
const BundleVersion = "v2"

// AssetsVersion tags the payload format of bundle revisions that carry
// assets (templates, static files). A bundle without assets stays v2, byte for
// byte, so revisions signed before assets existed keep verifying.
const AssetsVersion = "v3"

// BundleChecksum is the SHA-256 (hex) of a bundle's records, in the order
// given (Bundle order is path order): for each file, its path, a NUL byte,
// the decimal byte length of the content, a NUL byte and the content. The
// length prefix makes the encoding unambiguous, so renaming a file or moving
// bytes from one file to its neighbour changes the checksum even though the
// joined Source stays the same.
func BundleChecksum(files []platform.BundleFile) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00", f.Path, len(f.Content))
		h.Write([]byte(f.Content))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AssetsChecksum is the SHA-256 (hex) of a v3 revision: the bundle's records
// and the assets' records, each section introduced by a fixed label and its
// record count. The counts and labels make the boundary between the sections
// unambiguous, so moving bytes between a BCL file and an asset, renaming an
// asset, or dropping the assets all change the checksum.
func AssetsChecksum(files, assets []platform.BundleFile) string {
	h := sha256.New()
	writeRecords := func(label string, recs []platform.BundleFile) {
		fmt.Fprintf(h, "%s\x00%d\x00", label, len(recs))
		for _, f := range recs {
			fmt.Fprintf(h, "%s\x00%d\x00", f.Path, len(f.Content))
			h.Write([]byte(f.Content))
		}
	}
	h.Write([]byte("ref-bundle/" + AssetsVersion + "\x00"))
	writeRecords("bcl", files)
	writeRecords("assets", assets)
	return hex.EncodeToString(h.Sum(nil))
}

func sourceChecksum(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

// IsBundle reports whether the revision carries files.
func (r *Revision) IsBundle() bool { return len(r.Files) > 0 }

// HasAssets reports whether the revision carries assets (templates, static
// files) and is therefore a v3 revision.
func (r *Revision) HasAssets() bool { return len(r.Assets) > 0 }

// payloadVersion is the version tag its signatures use: v1 for a legacy
// revision, v2 for a bundle, v3 for a bundle with assets.
func (r *Revision) payloadVersion() string {
	switch {
	case r.HasAssets():
		return AssetsVersion
	case r.IsBundle():
		return BundleVersion
	}
	return "v1"
}

// AssetsFS is the revision's assets over base (typically os.DirFS of the
// application's resources directory): an asset wins over the base file with
// the same path. With no assets it is base itself.
func (r *Revision) AssetsFS(base fs.FS) fs.FS {
	if !r.HasAssets() {
		return base
	}
	return platform.Assets(r.Assets).Overlay(base)
}

// contentChecksum re-derives what the revision's checksum must be from its
// content, refusing a bundle whose files are malformed, unsorted, or do not
// join into Source.
func (r *Revision) contentChecksum() (string, error) {
	if !r.IsBundle() {
		if r.HasAssets() {
			return "", fmt.Errorf("%w: assets without files", ErrTampered)
		}
		return sourceChecksum([]byte(r.Source)), nil
	}
	b, err := platform.NewBundle(r.Files)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTampered, err)
	}
	if !slices.Equal(b, platform.Bundle(r.Files)) {
		return "", fmt.Errorf("%w: the revision's files are not in canonical order", ErrTampered)
	}
	if r.Source != string(b.Source()) {
		return "", fmt.Errorf("%w: the revision's source is not its files joined", ErrTampered)
	}
	if !r.HasAssets() {
		return BundleChecksum(b), nil
	}
	a, err := platform.NewAssets(r.Assets)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTampered, err)
	}
	if !slices.Equal(a, platform.Assets(r.Assets)) {
		return "", fmt.Errorf("%w: the revision's assets are not in canonical order", ErrTampered)
	}
	return AssetsChecksum(b, a), nil
}

// FileChange is one file that differs between a revision and the one it was
// proposed against.
type FileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "added", "modified" or "removed"
}

// diffFiles lists the files of next that differ from base, plus the ones
// base had that next dropped, in path order. A base without files (a legacy
// revision, or none) has no file identity to compare, so every file of next
// counts as added.
func diffFiles(base, next []platform.BundleFile) []FileChange {
	old := make(map[string]string, len(base))
	for _, f := range base {
		old[f.Path] = f.Content
	}
	var out []FileChange
	seen := map[string]bool{}
	for _, f := range next {
		seen[f.Path] = true
		prev, ok := old[f.Path]
		switch {
		case !ok:
			out = append(out, FileChange{f.Path, "added"})
		case prev != f.Content:
			out = append(out, FileChange{f.Path, "modified"})
		}
	}
	for _, f := range base {
		if !seen[f.Path] {
			out = append(out, FileChange{f.Path, "removed"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func clonedFiles(files []platform.BundleFile) []platform.BundleFile {
	if len(files) == 0 {
		return nil
	}
	return slices.Clone(files)
}
