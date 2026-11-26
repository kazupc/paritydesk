//go:build race

package app

// raceEnabled reports a -race build; allocation tests skip under it.
const raceEnabled = true
