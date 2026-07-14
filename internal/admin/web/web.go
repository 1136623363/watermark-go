package web

import (
	"embed"
	"io/fs"
)

//go:embed templates/*.html
var templateFiles embed.FS

func TemplatesFS() (fs.FS, error) {
	return fs.Sub(templateFiles, "templates")
}
