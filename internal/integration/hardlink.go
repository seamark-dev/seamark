package integration

// HardLinkReason is the blocker text of a file with more than one hard
// link (see HardLinked). Every editor of the file uses this one text, so
// the console states one reason for one cause.
const HardLinkReason = "replacing this file would break its hard link"
