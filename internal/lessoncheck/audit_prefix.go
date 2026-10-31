package lessoncheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// errRotatedDuringRead is the error of a read that met a rotation of the
// log twice. A third try cannot end the race. So the reader reports the
// error, and the delivery report shows the stream as unreadable.
var errRotatedDuringRead = errors.New("rotated during read")

// AuditPrefix is the result of ReadAuditPrefix, the audit stream reader
// that the delivery report uses. It states what the read found and what it
// left out, so a report can show the limits of its own reading.
type AuditPrefix struct {
	Path        string
	Generations int           // generation files opened: 0, 1, or 2 (rotated, then live)
	Bytes       int64         // bytes read: the sizes at open, cut at the bound
	Records     []AuditRecord // complete, parseable lines of schema AuditSchema
	Malformed   int           // complete lines that do not parse, have no schema, or exceed the line cap
	NewerSchema int           // lines with another schema value: kept out of Records
	PartialTail bool          // a prefix ends without "\n": that tail is ignored
	OverLimit   bool          // size above maxBytes: nothing beyond the limit is read
}

// ReadAuditPrefix reads the lesson-check audit stream of root as a
// bounded prefix. It reads the rotated generation first and the live
// file second. This is the order of writing. Both files share one
// maxBytes bound. A read of the live file alone would lose history
// without a sign each time a rotation moves records to the other file.
//
// Only a missing file counts as absent. The function returns every other
// error, because an unreadable stream is not an empty stream. A line
// that does not parse is never an error, because one corrupt append must
// not hide the other records. A negative maxBytes reads nothing. The
// read writes nothing: it creates no file or directory, takes no lock,
// and changes no mode. Only the writer uses the lock file.
//
// A rotation between the two opens costs one reopen. Two rotations in
// that short time need about maxAuditBytes of new writes. That is
// unlikely, so the reader does not detect them. It could then return a
// newer rotated generation and miss the live file that the second
// rotation removed, without an error. The reader accepts this risk.
func ReadAuditPrefix(root string, maxBytes int64) (AuditPrefix, error) {
	return readAuditPrefixAt(filepath.Join(root, ".seamark", auditFile), maxBytes, nil)
}

// readAuditPrefixAt is ReadAuditPrefix for an explicit live path. The
// between function runs with the attempt number, after the live file
// opens and before the rotated generation opens. Only a test sets it. It
// moves the files at the one moment that shows the race.
func readAuditPrefixAt(path string, maxBytes int64, between func(attempt int)) (AuditPrefix, error) {
	live, rotated, err := openGenerations(path, between)
	if err != nil {
		return AuditPrefix{}, fmt.Errorf("lesson-check audit: %w", err)
	}

	// Close both files on every path when the read ends.
	defer closeAll(live, rotated)

	out := AuditPrefix{Path: path}
	limit := max(maxBytes, 0)

	// The rotated generation comes first, because it holds the older
	// records.
	for _, gen := range []*os.File{rotated, live} {
		if gen == nil {
			continue
		}

		if err := out.readGeneration(gen, limit); err != nil {
			return AuditPrefix{}, fmt.Errorf("lesson-check audit: %w", err)
		}
	}

	return out, nil
}

// closeAll closes every non-nil file. A close error of a read-only file
// has no effect on the result, so the function ignores it.
func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// openGenerations opens the live file and then the rotated generation.
// It returns nil for a file that does not exist. The live file opens
// first. A rotation between the two opens then renames the open file to
// the rotated name, and the second open returns that same file.
// os.SameFile detects this case, and one more try reads the new state.
func openGenerations(path string, between func(attempt int)) (live, rotated *os.File, err error) {
	for attempt := range 2 {
		live, err = openRead(path)
		if err != nil {
			return nil, nil, err
		}

		if between != nil {
			between(attempt)
		}

		rotated, err = openRead(path + ".1")
		if err != nil {
			closeAll(live)

			return nil, nil, err
		}

		same, err := sameGeneration(live, rotated)
		if err != nil {
			closeAll(live, rotated)

			return nil, nil, err
		}

		if !same {
			return live, rotated, nil
		}

		closeAll(live, rotated)
	}

	return nil, nil, errRotatedDuringRead
}

// openRead opens an audit file for reading and refuses a symbolic link.
// A missing file gives a nil file and no error. The function returns
// every other error.
func openRead(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollowFlag, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	return f, err
}

// sameGeneration reports whether both open files are one file. A nil file
// is never the same file as another one.
func sameGeneration(a, b *os.File) (bool, error) {
	if a == nil || b == nil {
		return false, nil
	}

	ai, err := a.Stat()
	if err != nil {
		return false, err
	}

	bi, err := b.Stat()
	if err != nil {
		return false, err
	}

	return os.SameFile(ai, bi), nil
}

// readGeneration reads one open file into the prefix. limit is the
// bound of both generations together. The bytes that this file may use
// are limit minus p.Bytes, because p.Bytes counts every byte read so far.
func (p *AuditPrefix) readGeneration(f *os.File, limit int64) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}

	p.Generations++

	left := limit - p.Bytes
	if info.Size() > left {
		p.OverLimit = true
	}

	read, partial, err := reviews.ScanPrefix(f, min(info.Size(), left), p.add)
	if err != nil {
		return err
	}

	p.Bytes += read
	p.PartialTail = p.PartialTail || partial

	return nil
}

// add classifies one complete line and keeps it when it is a record of
// this stream. A blank line is not a record. The writer always sets the
// schema. So a line without a schema is corrupt or foreign data, and it
// counts as malformed. Another schema value counts as newer.
//
// Almost every line is a current record, so the function decodes the
// full record first, and one decode is enough for it. Only a line that
// fails that decode gets a second decode of the schema alone. So a newer
// record, whose other fields can have another type, counts as newer and
// not as malformed.
func (p *AuditPrefix) add(_ int, raw []byte, tooLong bool) {
	if tooLong {
		p.Malformed++

		return
	}

	if len(raw) == 0 {
		return
	}

	var rec AuditRecord
	if json.Unmarshal(raw, &rec) == nil {
		switch rec.Schema {
		case "":
			p.Malformed++
		case AuditSchema:
			p.Records = append(p.Records, rec)
		default:
			p.NewerSchema++
		}

		return
	}

	// The full decode failed: the line is not JSON, or a field has
	// another type. A newer schema can change a field type, so only the
	// schema value decides between newer and malformed.
	var head struct {
		Schema string `json:"schema"`
	}

	if json.Unmarshal(raw, &head) == nil && head.Schema != "" && head.Schema != AuditSchema {
		p.NewerSchema++

		return
	}

	p.Malformed++
}
