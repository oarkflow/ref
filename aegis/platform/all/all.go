// Package all imports all standard Aegis platform backend adapters to register
// them with the platform default registry.
//
// Importing this package anonymously ensures that "desktop", "android", "apple",
// "wasm", and "confidential" execution backends are available in [platform.DefaultRegistry].
package all

import (
	_ "github.com/oarkflow/ref/aegis/platform/android"
	_ "github.com/oarkflow/ref/aegis/platform/apple"
	_ "github.com/oarkflow/ref/aegis/platform/confidential"
	_ "github.com/oarkflow/ref/aegis/platform/desktop"
	_ "github.com/oarkflow/ref/aegis/platform/wasm"
)
