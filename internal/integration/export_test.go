package integration

// The test-only third client is defined in the in-package contract
// harness. The orchestration tests live in the external test package,
// because they import the packages that consume the registry, and an
// in-package test cannot import a consumer without an import cycle.
// These aliases hand the client to them; nothing here ships.

// ThirdID is the registry ID of the test-only third client.
const ThirdID = thirdID

// ThirdClient returns the test-only third client descriptor.
var ThirdClient = thirdClient

// RepeatedPathReasons returns each finding that names a path where the
// consumer prints one already. The matrix tests in the external package
// apply the same rule.
var RepeatedPathReasons = repeatedPathReasons
