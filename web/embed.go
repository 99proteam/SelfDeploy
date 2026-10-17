// Package web embeds the dashboard so SelfDeploy ships as a single binary.
package web

import "embed"

//go:embed static
var Files embed.FS
