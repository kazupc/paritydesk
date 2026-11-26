// Package fdscantest holds test helpers for the native open-file scan: a
// child process that holds files in every way the scan reports (run from
// a copy of the test binary inside the watched folder) and the parent
// side that starts it. Only test files import it; on platforms other
// than macOS, and under the build tag pd_nofdscan, the package is this
// file only.
package fdscantest
