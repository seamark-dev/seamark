package bench

import (
	"fmt"
	"path"
	"strings"
)

// WorkflowInstance is a benchmark problem for the skills workflow
// experiment. It wraps a lessons Instance and names the two files the
// experiment is about: the trigger file the task changes first, and the
// companion file the owner invariant lives in. The fixture history must
// carry the pair as a co-change, so `change_set` on the trigger can name
// the companion before the agent edits.
type WorkflowInstance struct {
	Instance

	// Trigger is the repository-relative file the visible task changes
	// first. The preflight asks `seamark why <trigger>` to name Companion.
	Trigger string
	// Companion is the repository-relative file the owner invariant
	// requires and the naive solution forgets.
	Companion string
}

// Validate rejects a workflow instance before it can spend an agent call.
func (w WorkflowInstance) Validate() error {
	if err := w.Instance.Validate(); err != nil {
		return err
	}

	for _, field := range []struct{ name, value string }{
		{"trigger", w.Trigger},
		{"companion", w.Companion},
	} {
		switch {
		case field.value == "":
			return fmt.Errorf("workflow instance %q has no %s file", w.ID, field.name)
		case field.value != path.Clean(field.value) || strings.HasPrefix(field.value, "/") ||
			strings.HasPrefix(field.value, "../") || strings.Contains(field.value, "\\"):
			return fmt.Errorf("workflow instance %q %s must be a clean repository-relative slash path, got %q",
				w.ID, field.name, field.value)
		}
	}

	if w.Trigger == w.Companion {
		return fmt.Errorf("workflow instance %q trigger and companion are the same file", w.ID)
	}

	return nil
}

// WorkflowInstances returns the stable catalogue of the skills workflow
// experiment: co-change variants of the three synthetic lessons fixtures.
// The pinned OpenTelemetry task is absent on purpose. Its prepared checkout
// is a single-commit clone with no history, so no co-change pair can exist
// in it and the preflight's co-change gate refuses it. Catalogue membership
// is outside every fingerprint, so adding an entry never invalidates an
// existing cohort.
func WorkflowInstances() []WorkflowInstance {
	return []WorkflowInstance{
		SchemaSyncCochangeInstance(),
		CacheVersionCochangeInstance(),
		ExportRegistryCochangeInstance(),
	}
}

// WorkflowInstanceIDs returns the CLI selectors in catalogue order.
func WorkflowInstanceIDs() []string {
	instances := WorkflowInstances()
	ids := make([]string, 0, len(instances))

	for _, instance := range instances {
		ids = append(ids, instance.ID)
	}

	return ids
}

// WorkflowInstanceByID resolves one workflow instance. There is no default:
// every paid workflow run names its instance explicitly.
func WorkflowInstanceByID(id string) (WorkflowInstance, error) {
	for _, instance := range WorkflowInstances() {
		if instance.ID == id {
			return instance, nil
		}
	}

	return WorkflowInstance{}, fmt.Errorf("unknown workflow instance %q (available: %s)",
		id, strings.Join(WorkflowInstanceIDs(), ", "))
}

// workflowVariant copies a lessons instance into a workflow variant. The
// task, judges, patches, checks, and lesson text stay identical, so the
// variant differs from its base only in the history the generator writes.
// The base file keeps its place as the instance source, because the judge
// lives there and a judge change must move the fingerprint; the history
// file is bound as the variant source, the way the lessons repair variants
// bind theirs. The lessons comparison family is dropped: the variant takes
// no part in the lessons delivery-scoping claim.
func workflowVariant(base Instance, id, variantSourceFile string, generate func(string) error) Instance {
	base.ID = id
	base.Generate = generate
	base.ComparisonFamily = ""
	base.ProtocolInstance = ""
	base.variantSourceFile = variantSourceFile

	return base
}
