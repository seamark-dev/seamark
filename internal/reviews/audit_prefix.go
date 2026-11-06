package reviews

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// AuditLine is one record of the firing log with its position. Line is
// the 1-based physical line number. A blank line before the record
// advances it, so a reader can point to the exact line of the file.
type AuditLine struct {
	Line   int
	Firing Firing
}

// AuditPrefix is the result of ReadAuditPrefix (contract L3). It states
// what the read found and what it left out. So a report can show the
// limits of its own reading. Lines holds every complete record of the
// current schema or an older one. Legacy counts a subset of Lines.
type AuditPrefix struct {
	Path string
	// Bytes is the number of bytes read: the file size at open, cut at
	// the maxBytes bound. The read stops there, also when the file grows.
	Bytes       int64
	Lines       []AuditLine // complete, parseable lines
	Legacy      int         // lines without schema_version
	Malformed   int         // complete lines that do not parse or exceed maxAuditLine
	NewerSchema int         // schema_version > AuditSchemaVersion: kept out of Lines
	PartialTail bool        // the prefix ends without "\n": that tail is ignored
	OverLimit   bool        // size above maxBytes: nothing beyond the limit is read
}

// ReadAuditPrefix reads the firing log of root as a bounded prefix. It
// reads at most min(size at open, maxBytes) bytes. So a log that grows
// during the read, or a log above the bound, cannot make the read
// unbounded. A negative maxBytes reads nothing.
//
// Only a missing log gives an empty prefix. The function returns every
// other open, stat, or read error, because an unreadable log is not an
// empty log (lesson error-is-not-absence). A line that does not parse is
// never an error, because the legacy readers also skip it. The read
// writes nothing.
func ReadAuditPrefix(root string, maxBytes int64) (AuditPrefix, error) {
	out := AuditPrefix{Path: AuditLogPath(root)}

	f, err := os.Open(out.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}

		return AuditPrefix{}, fmt.Errorf("firing log: %w", err)
	}
	defer func() { _ = f.Close() }()

	// One Stat of the open file fixes the size. A Stat of the path could
	// name a different file after a rotation.
	info, err := f.Stat()
	if err != nil {
		return AuditPrefix{}, fmt.Errorf("firing log: %w", err)
	}

	// Set a negative bound to zero first. An empty log is then never over
	// the limit.
	maxBytes = max(maxBytes, 0)
	out.OverLimit = info.Size() > maxBytes

	read, partial, err := ScanPrefix(f, min(info.Size(), maxBytes), out.add)
	if err != nil {
		return AuditPrefix{}, fmt.Errorf("firing log: %w", err)
	}

	out.Bytes = read
	out.PartialTail = partial

	return out, nil
}

// ScanPrefix reads the complete lines of r, at most limit bytes of it.
// It passes each line to fn with its 1-based line number. An over-long
// line arrives with tooLong set, and its bytes stop at the line bound.
// A tail without "\n" does not go to fn. It is incomplete, so it is
// neither a record nor a malformed line, and partialTail reports it. A
// negative limit reads nothing.
//
// Both audit logs use this one loop. So they apply the same bound, the
// same line numbers, and the same partial-tail rule. The loop calls
// readAuditLine, which the legacy readers still call directly.
func ScanPrefix(r io.Reader, limit int64, fn func(line int, raw []byte, tooLong bool)) (read int64, partialTail bool, err error) {
	lr := &io.LimitedReader{R: r, N: max(limit, 0)}
	start := lr.N
	br := bufio.NewReader(lr)

	for line := 1; ; line++ {
		raw, tooLong, rerr := readAuditLine(br, maxAuditLine)

		if rerr == io.EOF {
			return start - lr.N, len(raw) > 0 || tooLong, nil
		}

		if rerr != nil {
			return start - lr.N, false, rerr
		}

		fn(line, raw, tooLong)
	}
}

// add classifies one complete line and keeps it when it is a record.
// A blank line is not a record. The function decodes the schema first
// and alone. So a newer record, whose other fields can have another
// type, counts as newer and not as malformed.
func (p *AuditPrefix) add(line int, raw []byte, tooLong bool) {
	if tooLong {
		p.Malformed++

		return
	}

	if len(raw) == 0 {
		return
	}

	var head struct {
		SchemaVersion int `json:"schema_version"`
	}

	if json.Unmarshal(raw, &head) != nil || head.SchemaVersion < 0 {
		p.Malformed++

		return
	}

	if head.SchemaVersion > AuditSchemaVersion {
		p.NewerSchema++

		return
	}

	var rec Firing
	if json.Unmarshal(raw, &rec) != nil {
		p.Malformed++

		return
	}

	if head.SchemaVersion == 0 {
		p.Legacy++
	}

	p.Lines = append(p.Lines, AuditLine{Line: line, Firing: rec})
}
