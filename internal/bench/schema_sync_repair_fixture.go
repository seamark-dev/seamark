package bench

import "strings"

// SchemaSyncRepairInstanceID selects the delivery-scoping CONTROL: the
// schema-sync fixture with its pin scoped to the repair site.
const SchemaSyncRepairInstanceID = "python-ts-schema-sync-repair-v1"

// schemaSyncRepairRegion is where the pin's evidence lives — the
// generated client. Production pins were scoped here before v0.4.0:
// reviewers commented on the generated file, region inference followed
// the comments, and an agent editing the backend never matched the pin.
const schemaSyncRepairRegion = "web/src/api"

// SchemaSyncRepairInstance is SchemaSyncInstance with ONE difference:
// the pin's region moves from the trigger (server) to the repair site
// (web/src/api). The fixture, task, judges, patches, and the pin's
// note are shared by construction — the region swap is a textual
// replacement on the same yaml. The report subtracts each variant's hook-off
// baseline before attributing the remaining outcome delta to delivery scoping.
// This is the control variant of the lessons-delivery-scoping claim.
func SchemaSyncRepairInstance() Instance {
	instance := SchemaSyncInstance()

	instance.ID = SchemaSyncRepairInstanceID
	// A repair-scoped hook may legitimately see no matching editor action:
	// that absence is the old delivery behavior this control measures.
	instance.HookExposure = HookExposureOptional
	instance.LessonYAML = repairScoped(schemaSyncLessonYAML)
	instance.PlaceboYAML = repairScoped(schemaSyncPlaceboYAML)
	instance.variantSourceFile = "schema_sync_repair_fixture.go"

	return instance
}

// repairScoped rewrites the pin's one region line. A test asserts the
// swap happened and changed nothing else.
func repairScoped(yaml string) string {
	return strings.Replace(yaml, "region: server", "region: "+schemaSyncRepairRegion, 1)
}
