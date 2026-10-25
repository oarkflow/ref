package platform

import "time"

// Exported helpers for resource and action drivers that live in other modules
// (contrib/messaging, contrib/rtc). They are thin wrappers, so a driver reads
// its configuration with exactly the rules the built-in ones use.

// ConfigString returns config[key] as a string, or fallback when absent.
func ConfigString(config map[string]any, key, fallback string) string {
	return configString(config, key, fallback)
}

// ConfigStrings returns config[key] as a list of strings.
func ConfigStrings(config map[string]any, key string) []string { return configStrings(config, key) }

// ConfigInt returns config[key] as a whole number, rejecting fractions.
func ConfigInt(config map[string]any, key string, fallback int) (int, error) {
	return configInt(config, key, fallback)
}

// ConfigDuration returns config[key] as a duration ("45s", 45, or a duration
// literal).
func ConfigDuration(config map[string]any, key string, fallback time.Duration) (time.Duration, error) {
	return configDuration(config, key, fallback)
}

// ConfigBool returns config[key] as a bool.
func ConfigBool(config map[string]any, key string, fallback bool) bool {
	return configBool(config, key, fallback)
}

// RejectUnknownConfig fails on a config key the driver does not know, so a typo
// is an error at load time rather than a silently ignored setting.
func RejectUnknownConfig(what string, config map[string]any, known ...string) error {
	return rejectUnknownConfig(what, config, known...)
}

// Rebind converts $n placeholders to the dialect's form (only MySQL differs).
func Rebind(dialect, statement string) string { return rebind(dialect, statement) }

// FactString reads a fact by dotted path as a string.
func FactString(inputs map[string]any, path string) (string, error) { return factString(inputs, path) }

// Acknowledge returns the node's single output, or nothing if it declares none.
func Acknowledge(spec NodeSpec, value any) ActionResult { return acknowledgement(spec, value) }
