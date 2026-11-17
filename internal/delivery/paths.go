package delivery

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"
)

// maxRejections bounds the rejected paths one outcome keeps. A large
// patch can name thousands of paths, and a diagnostic must stay small.
const maxRejections = 8

// maxLinkHops bounds the links that one path follows. Operating systems
// use a limit of the same size, and a loop of links ends at the limit.
const maxLinkHops = 40

// RejectReason says why a native path gives no workspace file.
type RejectReason string

// The reasons NormalizePaths rejects a path.
const (
	// RejectMalformed is an empty path or a path with a NUL byte.
	RejectMalformed RejectReason = "malformed path"
	// RejectOutside is a path that resolves outside the workspace.
	RejectOutside RejectReason = "outside the workspace"
	// RejectUnresolvable is a path whose real location is unknown: the
	// links form a loop, or a directory on the way is not readable.
	RejectUnresolvable RejectReason = "real location is not resolvable"
)

// RejectedPath is one native path that selection does not use. Path is
// the raw client value: sanitize it before display.
type RejectedPath struct {
	Path   string
	Reason RejectReason
}

// Normalized is the result of NormalizePaths.
type Normalized struct {
	// Files lists the workspace files, repository-relative with forward
	// slashes, in first-appearance order and without duplicates.
	Files []string
	// Rejected holds the first maxRejections rejected paths.
	Rejected []RejectedPath
	// RejectedCount counts every rejected path, kept or not.
	RejectedCount int
}

// NormalizePaths turns the native paths of one edit event into
// repository-relative files. A relative path resolves against cwd, the
// working directory of the event. An empty cwd means the workspace
// root, and a relative cwd resolves against the workspace root.
//
// A path does not need to exist: new, deleted, and moved files are
// still in scope by path. The function follows every link of a path,
// in the order the operating system follows them. It rejects a path
// whose real location is outside the workspace, so a link never selects
// lessons for an unrelated tree. The result names the real location,
// because lessons follow the real file.
//
// The function reports each rejected path. The caller gets the count of
// all rejections and the first few rejected paths.
func NormalizePaths(root, cwd string, paths []string) Normalized {
	return normalizePaths(root, cwd, paths, true)
}

// NormalizeEntryPaths resolves parent links and keeps the final entry.
// Use it when an operation removes an entry rather than writes through it.
// Missing entries, parent steps, and rejection rules match NormalizePaths.
func NormalizeEntryPaths(root, cwd string, paths []string) Normalized {
	return normalizePaths(root, cwd, paths, false)
}

func normalizePaths(root, cwd string, paths []string, followFinal bool) Normalized {
	var out Normalized

	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}

	// The root resolves by the same rule as every path, so a link above
	// the workspace (/tmp on macOS) never makes an inside path look outside.
	realRoot, ok := realLocation(root)
	if !ok {
		realRoot = filepath.Clean(root)
	}

	// joinRaw also covers the empty cwd: the walk skips the empty name.
	if !filepath.IsAbs(cwd) {
		cwd = joinRaw(root, cwd)
	}

	for _, native := range paths {
		rel, reason := normalizePath(realRoot, cwd, native, followFinal)
		if reason != "" {
			out.RejectedCount++

			if len(out.Rejected) < maxRejections {
				out.Rejected = append(out.Rejected, RejectedPath{Path: native, Reason: reason})
			}

			continue
		}

		// Order stays stable, so equal-rank lessons keep a reproducible
		// order between runs of the same event.
		if !slices.Contains(out.Files, rel) {
			out.Files = append(out.Files, rel)
		}
	}

	return out
}

// normalizePath resolves one native path. realRoot is the workspace
// root with its links resolved. The second result is empty on success.
func normalizePath(realRoot, cwd, native string, followFinal bool) (string, RejectReason) {
	if native == "" || strings.ContainsRune(native, 0) {
		return "", RejectMalformed
	}

	abs := native
	if !filepath.IsAbs(abs) {
		abs = joinRaw(cwd, abs)
	}

	location, ok := walkLocation(abs, followFinal)
	if !ok {
		return "", RejectUnresolvable
	}

	rel, err := filepath.Rel(realRoot, location)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// The workspace root itself is a directory and never an edited
		// file, so it is outside the set of files too.
		return "", RejectOutside
	}

	return filepath.ToSlash(rel), ""
}

// joinRaw joins two path parts and keeps every ".." name. filepath.Join
// cleans its result, and a clean path has lost the ".." names that
// realLocation must apply after it follows a link.
func joinRaw(dir, path string) string {
	return dir + string(filepath.Separator) + path
}

// realLocation returns the location that an absolute path reaches on
// the file system. It walks the path one name at a time, as the
// operating system does, and follows each link when it meets the link.
//
// The order is the reason for the walk. A ".." after a link means the
// parent of the link target, not the parent of the link. filepath.Clean
// removes "link/.." before any link resolves. "link/../x" then looks
// like "x" inside the workspace, while the edit reaches a file outside.
//
// A name that exists gets its on-disk spelling (see onDiskName). A name
// that does not exist holds no link and no alias, so the walk keeps it
// as written. A link without a target gives the location of the missing
// target, because a write through that link creates the target.
//
// ok is false when the real location is unknown: the links form a loop,
// or the function cannot read a directory or a link on the way. A name
// whose on-disk spelling cannot be settled also gives false.
func realLocation(path string) (location string, ok bool) {
	return walkLocation(path, true)
}

// walkLocation optionally keeps the final entry instead of its link target.
// All preceding names still resolve in filesystem order, including "..".
func walkLocation(path string, followFinal bool) (location string, ok bool) {
	volume := filepath.VolumeName(path)
	location = volume + string(filepath.Separator)
	pending := pathNames(path[len(volume):])

	for hops := 0; len(pending) > 0; {
		name := pending[0]
		pending = pending[1:]

		switch name {
		case ".":
			continue
		case "..":
			// location holds no link, so its parent by name is its real parent.
			location = filepath.Dir(location)

			continue
		}

		candidate := filepath.Join(location, name)

		info, err := os.Lstat(candidate)
		if err == nil {
			// The lookup succeeded, but a case-insensitive file system
			// also accepts a spelling that differs from the entry.
			spelled, settled := onDiskName(location, name, info)
			if !settled {
				return "", false
			}

			candidate = filepath.Join(location, spelled)
		}

		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			// ENOTDIR means a regular file is in the directory position,
			// so the name below it is absent.
			location = candidate

			continue
		case err != nil:
			// An unreadable directory can hide a link that leaves the workspace.
			return "", false
		case !followFinal && len(pending) == 0:
			return candidate, true
		case info.Mode()&os.ModeSymlink == 0:
			location = candidate

			continue
		}

		hops++

		if hops > maxLinkHops {
			return "", false
		}

		target, err := os.Readlink(candidate)
		if err != nil {
			return "", false
		}

		// An absolute target restarts the walk at its volume root. A
		// relative target continues from the directory of the link.
		if filepath.IsAbs(target) {
			volume = filepath.VolumeName(target)
			location = volume + string(filepath.Separator)
			target = target[len(volume):]
		}

		pending = append(pathNames(target), pending...)
	}

	return location, true
}

// onDiskName returns the spelling that the directory dir stores for the
// existing entry name, and false when it cannot settle that spelling.
//
// Why: a case-insensitive file system, such as the default one on macOS
// and Windows, resolves "GENERATED" to the entry "generated". A write
// then reaches the file under the on-disk name, and the same name is the
// one that git and the lesson scopes use. A match by the spelling of the
// client would let a case variant leave a scope that the file is in.
//
// The function reads the directory only when the lookup of a case
// variant of name also succeeds, or when name has non-ASCII bytes, which
// a file system can also alias by Unicode normalization. A case-sensitive
// file system then costs one extra lookup per name. The entry is the
// one that is the same file as info. A directory that cannot be listed,
// or more than one matching entry, gives false (fail closed).
func onDiskName(dir, name string, info os.FileInfo) (string, bool) {
	if !mayAlias(dir, name) {
		return name, true
	}

	f, err := os.Open(dir)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	names, err := f.Readdirnames(-1)
	if err != nil {
		return "", false
	}

	if slices.Contains(names, name) {
		return name, true
	}

	// Case variants come first. Other entries are the Unicode
	// normalization case, where no case folding relates the names.
	candidates := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return !strings.EqualFold(n, name) })
	if len(candidates) == 0 {
		candidates = names
	}

	var found []string

	for _, n := range candidates {
		entry, err := os.Lstat(filepath.Join(dir, n))
		if err == nil && os.SameFile(info, entry) {
			found = append(found, n)
		}
	}

	if len(found) != 1 {
		return "", false
	}

	return found[0], true
}

// mayAlias reports whether the lookup of name in dir can reach an entry
// of another spelling. It probes with the other case of the name, and
// every probe error except "not found" counts as a possible alias.
func mayAlias(dir, name string) bool {
	for i := 0; i < len(name); i++ {
		if name[i] >= utf8.RuneSelf {
			return true
		}
	}

	probe := strings.ToUpper(name)
	if probe == name {
		probe = strings.ToLower(name)
	}

	if probe == name {
		return false
	}

	_, err := os.Lstat(filepath.Join(dir, probe))

	return !errors.Is(err, fs.ErrNotExist)
}

// pathNames splits a path into its names and drops the empty names
// that a leading, trailing, or doubled separator gives.
func pathNames(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool {
		return r < utf8.RuneSelf && os.IsPathSeparator(uint8(r))
	})
}
