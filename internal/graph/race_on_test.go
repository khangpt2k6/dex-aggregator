//go:build race

package graph

// raceDetectorEnabled reports whether the binary was built with -race.
//
// The race detector instruments every memory access, which inflates router
// latency by more than tenfold. Any timing assertion has to know that, or it
// measures the detector rather than the code.
const raceDetectorEnabled = true
