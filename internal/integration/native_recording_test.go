package integration

// This file holds the types and helpers that read the manifest of a
// native recording. The guard and timing tests read the manifest too, so
// these helpers are not in the capture recording checks.

// nativeManifest is the manifest.json of one recording. The sanitizer
// (scripts/capture-native-sanitize.py) writes it.
type nativeManifest struct {
	Client        string `json:"client"`
	ClientVersion string `json:"client_version"`
	Placeholders  struct {
		Workspace       string `json:"workspace"`
		Home            string `json:"home"`
		User            string `json:"user"`
		Seamark         string `json:"seamark"`
		AttachmentBytes string `json:"attachment_bytes"`
	} `json:"placeholders"`
	Hooks       []nativeHook `json:"hooks"`
	Transcripts []string     `json:"transcripts"`
	Extras      []string     `json:"extras"`
}

// nativeHook describes one recorded hook payload.
type nativeHook struct {
	File       string `json:"file"`
	Run        int    `json:"run"`
	Event      string `json:"event"`
	Bytes      int    `json:"bytes"`
	ProcessCWD string `json:"process_cwd"`
	ProjectDir string `json:"project_dir"`
	NativeMS   int    `json:"native_ms"`
}

// nativeString returns a string field of a decoded payload.
func nativeString(raw map[string]any, key string) string {
	s, _ := raw[key].(string)

	return s
}
