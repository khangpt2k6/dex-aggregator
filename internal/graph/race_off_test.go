//go:build !race

package graph

// raceDetectorEnabled reports whether the binary was built with -race.
const raceDetectorEnabled = false
