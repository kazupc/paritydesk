package rows

import "github.com/kazupc/paritydesk/internal/model"

// pendingV02 lists the v0.2 rows whose module lands with its own unit.
// Tests that cover every row of model.AllRows leave these out until
// then. Each unit deletes its own line (the blank lines keep the units'
// edits apart).
var pendingV02 = map[model.RowID]bool{
	model.RowScratch: true, // scratch unit

	model.RowSidebarCollapse: true, model.RowSidebarSeed: true, // sidebar-offline unit

	model.RowPins: true, // pins unit
}

// implementedRows returns model.AllRows without the pending v0.2 rows.
func implementedRows() []model.RowID {
	var out []model.RowID
	for _, r := range model.AllRows {
		if !pendingV02[r] {
			out = append(out, r)
		}
	}
	return out
}
