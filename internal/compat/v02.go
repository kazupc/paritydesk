package compat

// The v0.2 manifest tables are validated by one function per unit, each
// in its own file, so the units never edit the same lines of load.go.

// validateV02 runs the whole-manifest checks of the v0.2 tables. It is
// called last by validate.
func (m *Manifest) validateV02() error {
	for _, f := range []func() error{m.validateOnline, m.validateArtifacts, m.validateLegacy} {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

// validateRowV02 runs the per-row checks of the v0.2 row tables. It is
// called by validateRows for every row.
func validateRowV02(m *Manifest, r RowSpec) error {
	for _, f := range []func(*Manifest, RowSpec) error{validateRowIDB, validateRowScratch, validateRowSidebar} {
		if err := f(m, r); err != nil {
			return err
		}
	}
	return nil
}

// checkOverrideV02 runs the override rules of the v0.2 tables for one
// embedded manifest an override could replace.
func checkOverrideV02(base, over *Manifest) error {
	for _, f := range []func(*Manifest, *Manifest) error{checkOverrideOnline, checkOverrideIDB, checkOverrideScratch, checkOverrideArtifacts} {
		if err := f(base, over); err != nil {
			return err
		}
	}
	return nil
}
