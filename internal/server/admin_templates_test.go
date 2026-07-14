package server

import (
	"html/template"
	"testing"

	adminweb "watermark-backend/internal/admin/web"
)

func TestEmbeddedTemplatesParse(t *testing.T) {
	sub, err := adminweb.TemplatesFS()
	if err != nil {
		t.Fatalf("load template sub fs failed: %v", err)
	}
	if _, err := template.ParseFS(sub, "*.html"); err != nil {
		t.Fatalf("parse embedded templates failed: %v", err)
	}
}
