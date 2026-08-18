package bench

import (
	"fmt"
	"strings"
)

// Instances returns the stable benchmark catalogue. Values are constructed on
// demand so callers can safely customize them without mutating shared state.
// Catalogue membership is intentionally outside the execution fingerprint:
// adding an unrelated fixture must not invalidate an existing cohort.
func Instances() []Instance {
	return []Instance{
		SchemaSyncInstance(),
		SchemaSyncRepairInstance(),
		CacheVersionInstance(),
		ExportRegistryInstance(),
	}
}

// InstanceIDs returns the stable CLI selectors in catalogue order.
func InstanceIDs() []string {
	instances := Instances()
	ids := make([]string, 0, len(instances))

	for _, instance := range instances {
		ids = append(ids, instance.ID)
	}

	return ids
}

// InstanceByID resolves one CLI-facing benchmark instance. An empty selector
// retains the schema-sync instance as the backwards-compatible default.
func InstanceByID(id string) (Instance, error) {
	if id == "" {
		return SchemaSyncInstance(), nil
	}

	for _, instance := range Instances() {
		if instance.ID == id {
			return instance, nil
		}
	}

	return Instance{}, fmt.Errorf("unknown benchmark instance %q (available: %s)",
		id, strings.Join(InstanceIDs(), ", "))
}
