// Package roles embeds the built-in agent role definitions in this
// directory. Each <name>.md file is one role; see internal/role for the
// format.
package roles

import "embed"

// FS holds the role definition files.
//
//go:embed *.md
var FS embed.FS
