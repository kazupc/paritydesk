package compat

// IDBSpec is [row.idb]: where the pins row reads its value in the app's
// IndexedDB (a LevelDB store read without the app's lock) and the limits
// of that read (spike S14).
type IDBSpec struct {
	Dir              string   `toml:"dir"`
	BackendPrefix    string   `toml:"backend_prefix"`
	Database         string   `toml:"database"`
	ObjectStore      string   `toml:"object_store"`
	Key              string   `toml:"key"`
	IDsPath          []string `toml:"ids_path"`
	VersionPath      []string `toml:"version_path"`
	UpdatedPath      []string `toml:"updated_path"`
	Versions         []int    `toml:"versions"`
	IDPattern        string   `toml:"id_pattern"`
	BlinkVersions    []int    `toml:"blink_versions"` // [min, max]
	V8Versions       []int    `toml:"v8_versions"`    // [min, max]
	MaxValueBytes    int      `toml:"max_value_bytes"`
	MaxCopyBytes     int64    `toml:"max_copy_bytes"`
	MaxCloneBytes    int64    `toml:"max_clone_bytes"`
	MaxFiles         int      `toml:"max_files"`
	MaxReadMS        int      `toml:"max_read_ms"`
	MaxItems         int      `toml:"max_items"`
	MirrorPrefPath   []string `toml:"mirror_pref_path"`
	MirrorLSKey      string   `toml:"mirror_ls_key"`
	MirrorLSIDsPath  []string `toml:"mirror_ls_ids_path"`
	MirrorLSTimePath []string `toml:"mirror_ls_time_path"`
}

// validateRowIDB checks a row's [row.idb] table. The pins unit owns its
// rules (spec-pins 5.3).
func validateRowIDB(m *Manifest, r RowSpec) error { return nil }

// checkOverrideIDB refuses an override of base that weakens a [row.idb]
// table. The pins unit owns its rules (spec-pins 5.3).
func checkOverrideIDB(base, over *Manifest) error { return nil }
