package artifacts

import "flag"

// update rewrites the golden files of this package.
var update = flag.Bool("update", false, "rewrite golden files")
