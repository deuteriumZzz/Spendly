// Package web embeds the panel page served by the API.
package web

import "embed"

//go:embed index.html
var FS embed.FS
