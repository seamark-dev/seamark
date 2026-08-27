package bench

import "strings"

// OTelHistogramRepairInstanceID selects the repair-scoped control for the
// pinned OpenTelemetry Go histogram-reset task.
const OTelHistogramRepairInstanceID = "opentelemetry-go-histogram-reset-repair-v1"

// OTelHistogramRepairInstance differs from OTelHistogramInstance only by
// placing the lesson at the exponential-histogram repair path. An agent that
// edits only the explicit implementation therefore receives no reminder.
func OTelHistogramRepairInstance() Instance {
	instance := OTelHistogramInstance()
	instance.ID = OTelHistogramRepairInstanceID
	instance.HookExposure = HookExposureOptional
	instance.LessonYAML = otelHistogramRepairScoped(instance.LessonYAML)
	instance.PlaceboYAML = otelHistogramRepairScoped(instance.PlaceboYAML)
	instance.variantSourceFile = "otel_histogram_repair_fixture.go"

	return instance
}

func otelHistogramRepairScoped(yaml string) string {
	return strings.Replace(yaml, "region: "+otelHistogramTrigger,
		"region: "+otelHistogramRepair, 1)
}
