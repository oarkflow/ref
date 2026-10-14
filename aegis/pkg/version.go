package pkg

// Format identification constants for Aegis packages.
const (
	// FormatVersion is the current version of the Aegis package binary format.
	FormatVersion = 1

	// Magic is the 4-byte identification header for Aegis packages ("APKG").
	Magic = "APKG"

	// FormatName is the human-readable description of the package format.
	FormatName = "Aegis Package"
)
