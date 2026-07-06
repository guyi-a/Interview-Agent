package approval

// IsDestructive names tool calls that must always route to human approval,
// regardless of Mode — including full_access. The intent is a cross-cutting
// safety wall around irrecoverable operations (rm -rf, DROP TABLE, git
// reset --hard, etc.) so a user who elevated the mode can't accidentally
// green-light data loss with one click.
//
// MVP is intentionally empty because Interview-Agent's current tool surface
// (write_file / edit_file / write_file_chunked / read_file / list_files /
// mkdir / browser_use / browser_bridge / load_skill / extract_document_text)
// contains no genuinely destructive primitive — every write can be undone by
// re-writing, every browser action leaves a visible tab. When shell / delete
// tools land, wire their name into the switch and add the argument-matching
// helpers (see internal/approval/policy.go for the shape).
//
// Returns (isDestructive, humanReadableReason). The reason is logged, not
// shown to the model.
func IsDestructive(name, argsJSON string) (bool, string) {
	switch name {
	// TODO(shell): match `rm`, `shred`, `kill`, `pkill`, `truncate -s 0`,
	// `dd of=`, `> file` (truncating redirect), `git clean -fdx`,
	// `git reset --hard`, `git push --force`, `chmod 777`, `curl | bash`,
	// `wget && sh`. See PentaLoom infra/approval/cmd_classify.py for the
	// pattern set. Break on &&, ||, ;, | and scan each segment.
	// TODO(delete): match delete_file / remove_file / rmdir / batch delete.
	default:
		return false, ""
	}
}
