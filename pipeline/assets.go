package pipeline

import _ "embed"

// LivenessCaptureJS is the browser module that captures webcam frames for a
// liveness node (see assets/liveness-capture.js). Serve it as a static asset.
//
//go:embed assets/liveness-capture.js
var LivenessCaptureJS []byte
