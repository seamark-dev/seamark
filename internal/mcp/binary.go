package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/render"
)

// executable returns the launch path of this process. It is a variable,
// so a test can point the binary watch at a temporary file.
var executable = os.Executable

// binarySignature identifies the file that the launch path names. The
// target catches a moved symlink, as in a Homebrew upgrade. The file
// info catches a file that a rename or a copy replaced at the same path.
type binarySignature struct {
	target string
	info   os.FileInfo
}

// readSignature resolves path through its symlinks and reads the file
// at the target.
func readSignature(path string) (binarySignature, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return binarySignature{}, err
	}

	info, err := os.Stat(target)
	if err != nil {
		return binarySignature{}, err
	}

	return binarySignature{target: target, info: info}, nil
}

// same reports whether two signatures name the same unchanged file.
// SameFile catches a rename that keeps the size and the time, as with
// cp -p. The size and the time catch a write in place.
func (a binarySignature) same(b binarySignature) bool {
	return a.target == b.target &&
		os.SameFile(a.info, b.info) &&
		a.info.Size() == b.info.Size() &&
		a.info.ModTime().Equal(b.info.ModTime())
}

// binaryWatch compares the seamark binary on disk with the binary that
// this server started from. An MCP server lives as long as the agent
// session, so it keeps running the old binary after an upgrade.
type binaryWatch struct {
	// path is the launch path as os.Executable returned it. It keeps the
	// symlink, because a Homebrew upgrade moves only the symlink.
	path string
	// start is the signature at server start. A watch without a start
	// signature is off.
	start *binarySignature
	// logged is true after the log names the first change.
	logged bool
}

// startBinaryWatch reads the launch path and its signature once. A
// failure turns the watch off for the session, because no later compare
// can be trusted. The log names that failure once.
func startBinaryWatch(logf func(format string, a ...any)) binaryWatch {
	path, err := executable()
	if err != nil {
		logf("mcp: binary watch off: %v", err)

		return binaryWatch{}
	}

	sig, err := readSignature(path)
	if err != nil {
		logf("mcp: binary watch off: %v", err)

		return binaryWatch{}
	}

	return binaryWatch{path: path, start: &sig}
}

// note returns the call note about the binary, or "". The note repeats
// on every call after a change, because the server runs the old binary
// until the agent restarts it. Both texts name a restart, which always
// runs.
func (w *binaryWatch) note(version string, logf func(format string, a ...any)) string {
	if w.start == nil {
		return ""
	}

	path := render.Sanitize(w.path)

	var text string

	now, err := readSignature(w.path)

	switch {
	case err != nil:
		text = fmt.Sprintf("note: the seamark binary at %s was removed or moved after this MCP server started; "+
			"this server still runs seamark %s. Restart the agent or reconnect its MCP server to start the "+
			"seamark that is installed now.\n", path, version)
	case !w.start.same(now):
		text = fmt.Sprintf("note: the seamark binary at %s changed after this MCP server started; "+
			"this server still runs seamark %s. Restart the agent or reconnect its MCP server to use the "+
			"installed binary.\n", path, version)
	default:
		return ""
	}

	if !w.logged {
		w.logged = true
		logf("mcp: the seamark binary at %s changed after start; this server still runs seamark %s", path, version)
	}

	return text
}

// prefixNotes puts the call notes in front of the first text part of a
// tools/call result. The change_set_report key gets the same prefix,
// because a client that shows only structuredContent never sees the
// text parts.
func prefixNotes(result map[string]any, notes string) {
	if notes == "" {
		return
	}

	if content, ok := result["content"].([]map[string]any); ok && len(content) > 0 {
		text, _ := content[0]["text"].(string)
		content[0]["text"] = notes + text
	}

	if structured, ok := result["structuredContent"].(map[string]any); ok {
		if report, ok := structured["change_set_report"].(string); ok {
			structured["change_set_report"] = notes + report
		}
	}
}
