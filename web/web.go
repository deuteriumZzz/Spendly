// Package web embeds the panel (plain HTML/CSS/JS, no build step) served by the API.
package web

import "embed"

//go:embed index.html styles.css app.js logo.svg
var FS embed.FS
