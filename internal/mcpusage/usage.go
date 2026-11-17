// Package mcpusage keeps the local count of MCP tool calls in
// .seamark/mcp-usage.jsonl. The MCP server appends one record per
// tools/call, and `seamark lessons --stats` reads the records.
//
// A record holds the tool name, the client name, the time, the latency,
// the reply size, and the result. It holds no argument, path, symbol,
// query, or reply text, because a query can hold private text.
//
// A count measures calls. It does not show that a call helped.
package mcpusage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/privatedir"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
)

// Version is the value of the field v of each record. It is the 1 of
// the format name mcp-usage/1.
const Version = 1

// Unknown names a tool or a client that a record cannot name.
const Unknown = "unknown"

const (
	seamarkDir = ".seamark"
	usageFile  = "mcp-usage.jsonl"
	lockFile   = "mcp-usage.jsonl.lock"

	// privateFileMode is the mode of the sibling audit files.
	privateFileMode = 0o600

	// timeField names the field that the rotation reads for the record
	// age. The audit logs use "ts", so the rotation must get this name.
	timeField = "at"

	// maxUsageBytes and maxUsageAge repeat the limits of the gate audit
	// log, so total usage state stays near twice the size limit.
	maxUsageBytes = 5 << 20
	maxUsageAge   = 30 * 24 * time.Hour

	// maxNameBytes bounds a client name. The client chooses this text, so
	// one long name must not fill the log or the terminal.
	maxNameBytes = 64
)

// Record is one line of the usage log.
type Record struct {
	// V is Version. Append sets it.
	V int `json:"v"`
	// At is the UTC time of the call in RFC 3339. Append sets it.
	At string `json:"at"`
	// Tool is a known tool name or Unknown. The caller guarantees this,
	// because a name that no tool has is text from the client.
	Tool string `json:"tool"`
	// Client is the client name of initialize. Append cleans it.
	Client string `json:"client"`
	// MS is the latency in milliseconds.
	MS int64 `json:"ms"`
	// Bytes is the size of the text parts of the reply.
	Bytes int64 `json:"bytes"`
	// OK is false when the call returned an error or isError.
	OK bool `json:"ok"`
}

// Path returns the usage log of the workspace at root.
func Path(root string) string {
	return filepath.Join(root, seamarkDir, usageFile)
}

// CleanName returns name in the form that a record holds and a terminal
// can show: redacted, without control characters, on one line, and
// bounded. An empty result is Unknown.
func CleanName(name string) string {
	clean := strings.Join(strings.Fields(render.Sanitize(redact.Secrets(name))), " ")
	clean = render.CutBytes(clean, maxNameBytes)

	if clean == "" {
		return Unknown
	}

	return clean
}

// Append adds rec to the usage log, with now as its time. It uses the
// lock and the rotation of the audit logs, so concurrent servers append
// whole lines and a rotation loses no line.
//
// An absent .seamark gives nil and no line. The count must not create
// repository state, and that state is no failure. Each other failure
// returns, and the caller decides how to report it.
func Append(root string, rec Record, now time.Time) error {
	rec.V = Version
	rec.At = now.UTC().Format(time.RFC3339)
	rec.Client = CleanName(rec.Client)

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	dir := filepath.Join(root, seamarkDir)

	info, err := os.Lstat(dir)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		// A link to a directory is also no directory for Lstat. A cloned
		// repository can commit a link, and it must not redirect the log.
		return fmt.Errorf("mcpusage: %s is not a real directory", dir)
	}

	unlock, err := privatedir.LockLog(filepath.Join(dir, lockFile), privateFileMode, linkRefused)
	if err != nil {
		return err
	}
	defer unlock()

	path := filepath.Join(dir, usageFile)

	if err := privatedir.RefuseLink(path, linkRefused); err != nil {
		return err
	}

	if err := privatedir.RotateLogField(path, timeField, maxUsageBytes, maxUsageAge, privateFileMode, now); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|privatedir.NoFollow, privateFileMode)
	if err != nil {
		return err
	}

	// One Write is one line. With O_APPEND, two writers without the lock
	// (a platform without flock) still interleave whole lines.
	_, writeErr := f.Write(append(line, '\n'))

	return errors.Join(writeErr, f.Close())
}

// linkRefused builds the error for a symbolic link at a usage path.
func linkRefused(path string) error {
	return fmt.Errorf("mcpusage: %s is a symbolic link", path)
}

// Row sums the calls of one tool from one client.
type Row struct {
	Tool, Client string
	Calls        int
	Failed       int
	// MedianMS and MaxMS are milliseconds.
	MedianMS, MaxMS int64
	// MedianBytes is the median size of the text parts of a reply.
	MedianBytes int64
}

// Report is one read of the usage log and its rotated generation.
type Report struct {
	// Present is false when neither file exists. A report then prints
	// nothing, so output without a usage log stays the same.
	Present bool
	// Err holds a read failure. Rows are then empty, and the count is
	// unknown. It must never print as zero calls.
	Err error
	// Calls and Failed sum Rows.
	Calls, Failed int
	// Rows has the most calls first. Ties break on tool, then client.
	Rows []Row
	// Skipped counts the lines that do not parse as a record.
	Skipped int
}

// sample holds the raw values of one tool and client.
type sample struct {
	failed     int
	ms, nbytes []int64
}

// Read summarizes the retained records. The rotated generation comes
// first, because its records are older. A failed read of either file
// gives Err and no rows, because a part of the count reads as a low
// count.
func Read(root string) Report {
	var (
		report  Report
		samples = map[[2]string]*sample{}
	)

	for _, path := range []string{Path(root) + ".1", Path(root)} {
		found, err := readFile(path, samples, &report.Skipped)
		if err != nil {
			return Report{Present: true, Err: fmt.Errorf("read %s: %w", path, err)}
		}

		report.Present = report.Present || found
	}

	for key, s := range samples {
		row := Row{
			Tool: key[0], Client: key[1],
			Calls: len(s.ms), Failed: s.failed,
			MedianMS: median(s.ms), MaxMS: slices.Max(s.ms),
			MedianBytes: median(s.nbytes),
		}

		report.Calls += row.Calls
		report.Failed += row.Failed
		report.Rows = append(report.Rows, row)
	}

	slices.SortFunc(report.Rows, func(a, b Row) int {
		if a.Calls != b.Calls {
			return b.Calls - a.Calls
		}

		return strings.Compare(a.Tool+"\x00"+a.Client, b.Tool+"\x00"+b.Client)
	})

	return report
}

// readFile adds the records of one file to samples. It reports whether
// the file exists. A symbolic link gives an error, because the writer
// refuses a link too.
func readFile(path string, samples map[[2]string]*sample, skipped *int) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|privatedir.NoFollow, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }() // a read-only file has nothing to lose

	// A bufio.Reader has no line limit, so one long damaged line stops
	// nothing. The rotation bounds the file size.
	br := bufio.NewReader(f)

	for {
		line, err := br.ReadBytes('\n')

		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			addLine(trimmed, samples, skipped)
		}

		if errors.Is(err, io.EOF) {
			return true, nil
		}

		if err != nil {
			return false, err
		}
	}
}

// rawRecord decodes one line. The pointers tell an absent field from a
// zero value: an absent ok must not read as a failed call, and an absent
// ms must not read as a fast call.
type rawRecord struct {
	V      int    `json:"v"`
	At     string `json:"at"`
	Tool   string `json:"tool"`
	Client string `json:"client"`
	MS     *int64 `json:"ms"`
	Bytes  *int64 `json:"bytes"`
	OK     *bool  `json:"ok"`
}

// complete reports whether every field holds a value of a record of
// Version. A record of another version fails too, because its fields can
// have another meaning.
func (r rawRecord) complete() bool {
	if r.V != Version || r.Tool == "" || r.Client == "" || r.MS == nil || r.Bytes == nil || r.OK == nil {
		return false
	}

	if *r.MS < 0 || *r.Bytes < 0 {
		return false
	}

	_, err := time.Parse(time.RFC3339, r.At)

	return err == nil
}

// addLine adds one line to samples, or counts it in skipped.
func addLine(line []byte, samples map[[2]string]*sample, skipped *int) {
	var rec rawRecord

	if err := json.Unmarshal(line, &rec); err != nil || !rec.complete() {
		*skipped++

		return
	}

	// The file is local, so a person or a tool can edit it. The clean
	// step therefore runs on each read, before any terminal print.
	key := [2]string{CleanName(rec.Tool), CleanName(rec.Client)}

	s := samples[key]
	if s == nil {
		s = &sample{}
		samples[key] = s
	}

	s.ms = append(s.ms, *rec.MS)
	s.nbytes = append(s.nbytes, *rec.Bytes)

	if !*rec.OK {
		s.failed++
	}
}

// median returns the middle value of a non-empty list. An even list gives
// the mean of the two middle values, rounded down.
func median(values []int64) int64 {
	sorted := slices.Sorted(slices.Values(values))
	mid := len(sorted) / 2

	if len(sorted)%2 == 1 {
		return sorted[mid]
	}

	return (sorted[mid-1] + sorted[mid]) / 2
}
