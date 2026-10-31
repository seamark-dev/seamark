package integration

// Landed reports whether the file rel under root now holds the bytes
// with the SHA-256 want. An editor calls it after a guarded write that
// returned an error, because the write error alone does not prove the
// file state: a rename can succeed before a later step fails. The
// three results are the three outcomes that an editor reports:
//
//   - true and no error: the planned bytes are in the file (written);
//   - false and no error: the file holds other bytes or is absent (not
//     written);
//   - an error: the file cannot be read again, so the outcome is
//     unknown. The caller must not report "written" or "not written".
//
// It reads through ReadInput, so it follows a link for the read as the
// editors do, and it writes nothing. ReadInput already hashes an
// existing file, so the guard digest is the digest of the bytes read.
func Landed(root, rel string, want [32]byte) (bool, error) {
	guard, _, err := ReadInput(root, rel)
	if err != nil {
		return false, err
	}

	return guard.Exists && guard.SHA256 == want, nil
}

// SameGuard reports whether two observations of one file describe the
// same state: existence, content, permission bits, link, and write
// access. It skips Path and Kind, because they name the file and the
// read rule, not the file state. An editor compares the guard of a
// reviewed plan with a fresh observation right before it writes. A
// change of the mode or of the access after the review is also a change
// that the user did not see.
//
// The setup engine (verifyGuard) compares the whole guard too, but with
// the observation of its own setup plan, not with the reviewed guard.
// pinfile.Prepare compares only the existence and the content with the
// reviewed guard. So the editor compares the rest with SameGuard.
//
// The function compares the whole struct after it copies the two
// identity fields. So a state field that FileGuard gains later is
// compared too, and the check never skips it silently.
func SameGuard(a, b FileGuard) bool {
	a.Path, a.Kind = b.Path, b.Kind

	return a == b
}
