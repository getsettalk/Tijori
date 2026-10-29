// Package web embeds the admin panel's static files into the binary.
package web

import "embed"

// Admin holds web/admin: index.html, app.js, app.css and the logos.
//
//go:embed admin
var Admin embed.FS
