// Package seamark is the module root. It holds one thing: the agent
// skills embedded from the skills/ tree. The tree stays at the repository
// root because `npx skills add seamark-dev/seamark --skill <name>` looks
// for SKILL.md there. A go:embed directive cannot reach a parent
// directory, so the embedding package sits beside the tree.
package seamark

import "embed"

// SkillsFS is the skills/ tree, byte for byte, as built into the binary.
// The embed directive skips names that start with "." or "_", so the
// tree must carry none; a test under internal/skills compares the two.
// internal/skills is the only reader: init, doctor, and status go
// through it.
//
//go:embed skills
var SkillsFS embed.FS
