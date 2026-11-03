// Package deploy embeds the deploy scripts that egw-desktop runs elevated, so the elevated process
// executes the exact bytes compiled into egw-desktop.exe and never reads a script from the
// user-writable checkout. The embed directive reads these files at build time: editing update.ps1 or
// workspaces.ps1 requires rebuilding egw-desktop (there is no copy to drift).
package deploy

import _ "embed"

//go:embed update.ps1
var UpdateScript string

//go:embed workspaces.ps1
var WorkspacesScript string
