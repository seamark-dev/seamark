package lessoncheck

import (
	"fmt"
	"strings"
)

// This file reads the output of `git status --porcelain=v2 -z` and maps
// each record to a change candidate (contract K4, status v2 record
// mapping). The mapping reads no file. The producer in worktree.go then
// looks at the worktree and decides the final kind and flags.

// The git modes that the mapping compares.
const (
	modeAbsent  = "000000"
	modeFile    = "100644"
	modeExec    = "100755"
	modeSymlink = "120000"
	modeGitlink = "160000"
)

// statusRecord is one record of `git status --porcelain=v2 -z`.
type statusRecord struct {
	kind      byte   // '1' ordinary, '2' rename or copy, 'u' unmerged, '?' untracked, '!' ignored
	xy        string // the staged and the unstaged state, for example ".M"
	sub       string // "N..." for a path that is not a submodule, else "S<c><m><u>"
	modeHead  string // mH; empty for a 'u' record, whose HEAD side comes from ls-tree
	modeIndex string // mI
	modeTree  string // mW, the worktree mode
	oidHead   string // hH
	oidIndex  string // hI
	path      string
	origPath  string // the source path of a '2' record
}

// parseStatusV2 splits the NUL-separated status output into records. A
// path can hold spaces, so each header splits into its exact field count
// and the path is the rest. A record that does not parse is an error,
// because a partial parse must never read as a complete path set.
func parseStatusV2(out []byte) ([]statusRecord, error) {
	tokens := strings.Split(string(out), "\x00")

	// The output ends with a NUL, so the last token is empty.
	if n := len(tokens); n > 0 && tokens[n-1] == "" {
		tokens = tokens[:n-1]
	}

	var records []statusRecord

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "" {
			return nil, fmt.Errorf("lessoncheck: empty status record at token %d", i)
		}

		var (
			rec statusRecord
			err error
		)

		switch tok[0] {
		case '#':
			// A header line. The snapshot does not ask for one, so it
			// skips one that a git version prints anyway.
			continue
		case '1':
			rec, err = parseOrdinary(tok)
		case '2':
			if i+1 >= len(tokens) {
				return nil, fmt.Errorf("lessoncheck: rename record without its source path: %q", tok)
			}

			rec, err = parseRename(tok, tokens[i+1])
			i++
		case 'u':
			rec, err = parseUnmerged(tok)
		case '?', '!':
			rec, err = parseUntracked(tok)
		default:
			err = fmt.Errorf("lessoncheck: unknown status record %q", tok)
		}

		if err != nil {
			return nil, err
		}

		records = append(records, rec)
	}

	return records, nil
}

// withoutUntracked returns the status output without the untracked
// records whose path drop accepts. The other tokens keep their bytes and
// their order, so the digest of an output that holds no such record does
// not change. A rename record owns the token after it: that token is a
// source path and never a record of its own.
func withoutUntracked(out []byte, drop func(path string) bool) []byte {
	tokens := strings.Split(string(out), "\x00")

	// The output ends with a NUL, so the last token is empty.
	if n := len(tokens); n > 0 && tokens[n-1] == "" {
		tokens = tokens[:n-1]
	}

	var kept strings.Builder

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]

		switch {
		case tok != "" && tok[0] == '2' && i+1 < len(tokens):
			kept.WriteString(tok + "\x00" + tokens[i+1] + "\x00")

			i++
		case strings.HasPrefix(tok, "? ") && drop(tok[2:]):
		default:
			kept.WriteString(tok + "\x00")
		}
	}

	return []byte(kept.String())
}

// statusFields splits a record header into n fields and checks the
// submodule field and the path. The last field is the path.
func statusFields(tok string, n int) ([]string, error) {
	f := strings.SplitN(tok, " ", n)
	if len(f) != n || len(f[2]) != 4 || f[n-1] == "" {
		return nil, fmt.Errorf("lessoncheck: malformed status record %q", tok)
	}

	return f, nil
}

// parseOrdinary parses "1 XY sub mH mI mW hH hI path".
func parseOrdinary(tok string) (statusRecord, error) {
	f, err := statusFields(tok, 9)
	if err != nil {
		return statusRecord{}, err
	}

	return statusRecord{
		kind: '1', xy: f[1], sub: f[2],
		modeHead: f[3], modeIndex: f[4], modeTree: f[5],
		oidHead: f[6], oidIndex: f[7], path: f[8],
	}, nil
}

// parseRename parses "2 XY sub mH mI mW hH hI Xscore path" and the
// source path that follows it in the next token.
func parseRename(tok, origPath string) (statusRecord, error) {
	f, err := statusFields(tok, 10)
	if err != nil || origPath == "" {
		return statusRecord{}, fmt.Errorf("lessoncheck: malformed rename record %q", tok)
	}

	return statusRecord{
		kind: '2', xy: f[1], sub: f[2],
		modeHead: f[3], modeIndex: f[4], modeTree: f[5],
		oidHead: f[6], oidIndex: f[7], path: f[9], origPath: origPath,
	}, nil
}

// parseUnmerged parses "u XY sub m1 m2 m3 mW h1 h2 h3 path". The stage
// modes and ids are not the HEAD side, so the record keeps only mW.
func parseUnmerged(tok string) (statusRecord, error) {
	f, err := statusFields(tok, 11)
	if err != nil {
		return statusRecord{}, err
	}

	return statusRecord{kind: 'u', xy: f[1], sub: f[2], modeTree: f[6], path: f[10]}, nil
}

// parseUntracked parses "? path" and "! path".
func parseUntracked(tok string) (statusRecord, error) {
	if len(tok) < 3 || tok[1] != ' ' {
		return statusRecord{}, fmt.Errorf("lessoncheck: malformed status record %q", tok)
	}

	return statusRecord{kind: tok[0], path: tok[2:]}, nil
}

// candidate is one change before the producer reads its content. The
// status record gives the paths, the HEAD side, and a hint for the
// worktree side. The producer then looks at the worktree, reads the
// content, and decides the final kind and flags.
type candidate struct {
	path        string
	oldPath     string
	kind        ChangeKind
	move        MoveDetection
	limitations []string
	headMode    string // the mode of the before side in HEAD; "" means absent
	headOid     string // the object id of the before side in HEAD
	treeMode    string // the worktree mode that status gives; "" means the producer takes it from Lstat
	treeOid     string // the gitlink commit of the worktree side, when status gives it
	lstatRule   bool   // a '1' or '2' record with mW=000000 (contract K4, Lstat rule)
	needsHead   bool   // a 'u' record: the HEAD side comes from ls-tree
	dropIfSame  bool   // a '1' record: no content, mode, or type difference means no change
	untracked   bool   // a '?' or '!' record, so a candidate for move pairing
	throughLink bool   // an explicit path below a symbolic link
	// gitObserved is true for a '1' record whose content difference git
	// already observed: the worktree equals the index, and the index blob
	// differs from the HEAD blob. The producer then needs no read to
	// decide the content flag.
	gitObserved bool
}

// mapRecords applies the status v2 record mapping of contract K4 and
// returns the change candidates. A '?' or '!' record folds into a
// tracked record of the same path, because the tracked record owns that
// path (the Lstat rule). A path appears in at most one candidate. The
// caller decides the submodule limitation, because it depends on the
// selection.
func mapRecords(records []statusRecord) []*candidate {
	var cands []*candidate

	owned := map[string]bool{}

	for i := range records {
		rec := &records[i]

		var c *candidate

		switch rec.kind {
		case '1':
			c = mapOrdinary(rec)
			owned[rec.path] = true
		case '2':
			c = mapRename(rec)
			owned[rec.path] = true

			if c != nil && c.lstatRule {
				owned[rec.origPath] = true
			}
		case 'u':
			c = &candidate{path: rec.path, kind: ChangeModify, treeMode: rec.modeTree, needsHead: true,
				limitations: []string{LimitationUnmerged}}
			owned[rec.path] = true
		}

		if c != nil {
			cands = append(cands, c)
		}
	}

	for i := range records {
		rec := &records[i]
		if rec.kind != '?' && rec.kind != '!' {
			continue
		}

		// git prints a nested repository as one "? dir/" record.
		p := strings.TrimSuffix(rec.path, "/")
		if owned[p] {
			continue
		}

		owned[p] = true
		cands = append(cands, &candidate{path: p, kind: ChangeAdd, untracked: true})
	}

	return cands
}

// mapOrdinary maps one '1' record. It returns nil when the record is no
// change against HEAD.
func mapOrdinary(rec *statusRecord) *candidate {
	switch {
	case rec.modeHead == modeAbsent && rec.modeTree == modeAbsent:
		// Added to the index, then deleted from the worktree (AD).
		return nil
	case rec.modeTree == modeAbsent:
		return &candidate{path: rec.path, kind: ChangeDelete, lstatRule: true, headMode: rec.modeHead, headOid: rec.oidHead}
	case rec.modeHead == modeAbsent:
		return &candidate{path: rec.path, kind: ChangeAdd, treeMode: rec.modeTree, treeOid: gitlinkTreeOid(rec)}
	case rec.modeHead == modeGitlink || rec.modeTree == modeGitlink:
		// A submodule changes only when its commit or its mode changes.
		// Content inside it is not inspected (a snapshot limitation).
		if rec.modeHead == rec.modeTree && rec.oidHead == rec.oidIndex && !submoduleCommitChanged(rec.sub) {
			return nil
		}

		return &candidate{path: rec.path, kind: ChangeModify, headMode: rec.modeHead, headOid: rec.oidHead,
			treeMode: rec.modeTree, treeOid: gitlinkTreeOid(rec)}
	default:
		return &candidate{path: rec.path, kind: ChangeModify, headMode: rec.modeHead, headOid: rec.oidHead,
			treeMode: rec.modeTree, dropIfSame: true,
			gitObserved: len(rec.xy) == 2 && rec.xy[1] == '.' && rec.oidIndex != rec.oidHead}
	}
}

// mapRename maps one '2' record. A destination deleted in the worktree
// contributes nothing, and the source path takes the Lstat rule.
func mapRename(rec *statusRecord) *candidate {
	if rec.modeTree == modeAbsent {
		return &candidate{path: rec.origPath, kind: ChangeDelete, lstatRule: true, headMode: rec.modeHead, headOid: rec.oidHead}
	}

	return &candidate{path: rec.path, oldPath: rec.origPath, kind: ChangeMove, move: MoveGitRename,
		headMode: rec.modeHead, headOid: rec.oidHead, treeMode: rec.modeTree, treeOid: gitlinkTreeOid(rec)}
}

// gitlinkTreeOid returns the commit of a worktree gitlink when status
// gives it. Status gives the index commit, and the worktree submodule
// is at that commit unless the commit flag is set.
func gitlinkTreeOid(rec *statusRecord) string {
	if rec.modeTree != modeGitlink || submoduleCommitChanged(rec.sub) {
		return ""
	}

	return rec.oidIndex
}

// submoduleCommitChanged reports the "C" flag of a submodule field.
func submoduleCommitChanged(sub string) bool {
	return len(sub) == 4 && sub[0] == 'S' && sub[1] == 'C'
}

// submoduleContentChanged reports the "M" or "U" flag of a submodule
// field: tracked or untracked changes inside the submodule.
func submoduleContentChanged(sub string) bool {
	return len(sub) == 4 && sub[0] == 'S' && (sub[2] == 'M' || sub[3] == 'U')
}

// typeOfMode returns the object type of a git mode.
func typeOfMode(mode string) ObjectType {
	switch mode {
	case "", modeAbsent:
		return ObjectAbsent
	case modeFile, modeExec:
		return ObjectFile
	case modeSymlink:
		return ObjectSymlink
	case modeGitlink:
		return ObjectGitlink
	default:
		return ObjectOther
	}
}
