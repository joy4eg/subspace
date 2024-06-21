package web

import (
	"embed"
)

var (
	// Static is the embedded static files.
	//go:embed static
	Static embed.FS

	// Email is the embedded email templates.
	//go:embed email
	Email embed.FS

	// Templates is the embedded html templates.
	//go:embed templates
	Templates embed.FS
)
