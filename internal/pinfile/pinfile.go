// Package pinfile reads, classifies, and edits the pin list of
// .seamark/lessons.yaml. The console uses it for every pin edit. The
// shared apply of internal/proposals uses it for the CLI and for the
// console, and so do the retarget and the prune of internal/maintain. A
// pin is a current entry of that list. The file is the authority for
// the installed guidance, and the proposal ledger is the authority for
// recorded decisions. This package relates the two and never repairs
// one from the other.
//
// A read has two layers. The listing decodes the pin items of the first
// YAML document, so the console shows the installed guidance even when
// the file cannot be edited. The editing gate then decides whether a
// splice can keep every other byte. A gate failure sets a block reason;
// it never empties the listing, because an error is not an absence.
//
// Classify links each entry to the proposal rows by one canonical key
// (reviews.ProposalKey). Plan computes the complete candidate file for a
// retarget or a prune and proves it before it returns: the candidate
// loads, keeps the other settings, and changes only the reviewed
// entries. The line check states the D-7 and D-8 line rules again
// without the span code that built the splices; verify.go names its one
// exception. PlanAppend computes the candidate for an apply: the new
// entries go in at the head of the list, and every existing line stays.
// It also proves its candidate before it returns it. Prepare,
// PrepareAppend, and Apply write a candidate through the guarded setup
// engine (integration Route A). Restore puts the old bytes back only
// while the file still holds the written bytes.
//
// # Layouts
//
// The editor changes only the layouts whose lines it can splice and
// prove. Every other layout stays listed and read-only, and the block
// reason names its line, because an error is not an absence. The rules:
//
//   - Supported: the items of a block list under pin: at any consistent
//     indentation, keys in any order, comments and blank lines between
//     and inside items, regions as a one-line flow list or as a block
//     list with one value per line, quoted or plain values, and keys
//     that no reader uses.
//   - Blocks the whole file (yamledit and structuralGate): more than one
//     YAML document, a top level that is not a block mapping, CRLF or
//     bare CR line breaks, mixed line breaks, text that is not UTF-8, a
//     tab in the indentation, duplicate keys, a pin value that is not a
//     block list, a tag or an anchor in the pin section, an alias or a
//     merge key, an empty item, and a file that the lessons reader
//     rejects. A splice in such a file cannot prove that it keeps every
//     other byte.
//   - Blocks the write, not the plan (Prepare): a symbolic link on the
//     path, a file without the owner write bit, and a file that this
//     user cannot write, for example through an access control list.
//     Route A refuses to replace such a file.
//   - Blocks one entry (entryBlock, regionsBlock): a flow-style entry,
//     an alias entry, a rule or region value that is not text or spans
//     several lines, a flow regions list over several lines, and a block
//     regions item that is not one value on its own line.
//   - Blocks a retarget only: a key removal or a move that would drop or
//     separate a comment, and a region without a safe one-line YAML
//     form.
//
// An apply (PlanAppend) writes CRLF files and keeps their line breaks,
// so its refusals are narrower than the edit gate. It still refuses a
// pin value that is not a plain block list, for example pin: [], more
// than one YAML document, and mixed line breaks.
//
// # What an edit keeps
//
// A retarget changes only the region and regions lines of each reviewed
// entry, in place. It can add or remove a regions key, and it writes
// region: "*" for a repository-wide scope. A prune removes the lines of
// the entry and the provenance comments directly above it that name its
// proposal. The rule spelling, the live note, other comments, unknown
// keys, blank lines, the entry order, other pins, mutes and settings
// stay. A retarget target that another pin, another selected pin or an
// applied proposal already has is refused (ErrCollision), because the
// editor never merges or overwrites pins.
//
// The package reads the ledger rows that the caller passes in. It does
// not open the store, and it never writes the ledger.
package pinfile

import (
	"crypto/sha256"
	"errors"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// File is the repository-relative path of the pin file.
const File = ".seamark/lessons.yaml"

// Entry is one pin item of the file, in file order. The fields hold the
// decoded values, as reviews.Config reads them, so Key equals the key of
// the delivered pin.
type Entry struct {
	// Index is the position of the item in the pin sequence. Empty items
	// count, so Index follows the YAML text and can differ from the
	// position in reviews.Config.Pin.
	Index int
	// Line is the 1-based dash line. EndLine is the last line that the
	// entry owns. Both are exact when the file parses for editing; else
	// they are the first and the last line of its decoded values.
	Line, EndLine int
	// Rule, Region, Regions, and Note are the decoded pin fields.
	Rule, Region, Note string
	Regions            []string
	// Unknown lists the keys that no reader uses, in file order. They do
	// not block an edit; a retarget keeps them.
	Unknown []string
	// Raw is the exact text of the entry's own lines with LF breaks. It is
	// file data: a caller sanitizes it before display.
	Raw string
	// Key is the canonical identity of the pin as delivered.
	Key reviews.PinKey
	// Block is the reason the entry cannot be edited, or empty.
	Block string
	// ProvenanceIDs are the proposal IDs that seamark provenance comments
	// directly above the entry name, in file order. They are a display
	// hint; a link never depends on them.
	ProvenanceIDs []int64
}

// Ref returns the binding of the entry for a later edit: its position,
// its key, and the hash of its own lines.
func (e Entry) Ref() EntryRef {
	return EntryRef{Index: e.Index, Key: e.Key, RawSHA: sha256.Sum256([]byte(e.Raw))}
}

// Document is one read of the pin file.
type Document struct {
	// Guard is the observation from integration.ReadInput: existence,
	// content hash, mode, and a symbolic link on the path. Parse leaves it
	// zero.
	Guard integration.FileGuard
	// Bytes is the exact file content; nil for an absent file.
	Bytes []byte
	// Entries lists the pin items that decode as mappings, in file order.
	Entries []Entry
	// FileBlock is the reason no entry can be edited, or empty. A link or
	// a file that this user cannot write blocks the write but still lets
	// Plan compute the change for review.
	FileBlock string
	// DecodeErr is a YAML syntax error of the first document. The listing
	// is then empty.
	DecodeErr error
	// ReaderErr is the error of reviews.ParseConfig on Bytes. The listing
	// keeps every entry that still decodes.
	ReaderErr error
	// ListErr reports a listing that differs from the pins that the
	// reader delivers, for example through an alias pin value or a pin
	// key from a merge key. The listing then cannot show which pins are
	// absent.
	ListErr error
	// ErrLine is the 1-based line of DecodeErr or ReaderErr, or 0. The
	// text of ListErr names its line itself.
	ErrLine int
}

// Unreadable reports whether d gives no reliable listing: no document, a
// YAML syntax error, a lessons reader error, or a listing that differs
// from the delivered pins. No pin is then known to be absent (D-5).
// Classify and the maintenance planner both use it, so they agree.
func (d *Document) Unreadable() bool {
	return d == nil || d.DecodeErr != nil || d.ReaderErr != nil || d.ListErr != nil
}

// Parse reads data as the content of the pin file. It is pure. An empty
// input is an empty list, the same as an absent file.
func Parse(data []byte) *Document {
	a := analyze(data)

	return &Document{
		Bytes:     data,
		Entries:   a.entries,
		FileBlock: a.gate,
		DecodeErr: a.decodeErr,
		ReaderErr: a.readerErr,
		ListErr:   a.listErr,
		ErrLine:   a.errLine,
	}
}

// Read reads the pin file under root. It follows a symbolic link, as the
// readers do, and records it in the guard; the link then blocks the
// write. It returns an error only for a path that is not a regular file
// and for an I/O failure. An absent file gives an empty document.
func Read(root string) (*Document, error) {
	if root == "" {
		return nil, errors.New("pinfile: workspace root is empty")
	}

	guard, data, err := integration.ReadInput(root, File)
	if err != nil {
		return nil, err
	}

	d := Parse(data)
	d.Guard = guard

	// The engine owns the refusal of a link and of a file that this user
	// cannot write. The view shows the same reason before the user asks
	// for a write.
	if d.FileBlock == "" {
		d.FileBlock = integration.WriteRefusal(guard)
	}

	return d, nil
}
