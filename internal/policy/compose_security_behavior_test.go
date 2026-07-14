package policy_test

import (
	"slices"
	"testing"
)

func TestComposePolicyDetectsDocumentsByContentAndCanonicalPath(t *testing.T) {
	violations, err := composePolicyViolations([]byte("services:\n  api:\n    build: .\n"), "deploy/stack.yml")
	if err != nil {
		t.Fatalf("scan Compose document: %v", err)
	}
	for _, required := range []string{"noncanonical-compose-path", "service-build"} {
		if !slices.Contains(violations, required) {
			t.Errorf("violations = %v, missing %s", violations, required)
		}
	}

	violations, err = composePolicyViolations([]byte("services:\n  api:\n    image: example.invalid/api@sha256:deadbeef\n"), "deploy/compose.yml")
	if err != nil || len(violations) != 0 {
		t.Fatalf("canonical image-only Compose was rejected: violations=%v error=%v", violations, err)
	}

	violations, err = composePolicyViolations([]byte("kind: workflow\nsteps: []\n"), "automation.yml")
	if err != nil || len(violations) != 0 {
		t.Fatalf("non-Compose YAML was rejected: violations=%v error=%v", violations, err)
	}
}

func TestComposePolicyRejectsMergedBuildIncludeAndExtends(t *testing.T) {
	fixtures := []struct {
		name string
		body string
		want string
	}{
		{
			name: "anchor merge",
			body: "x-base: &base\n  build: .\nservices:\n  api:\n    <<: *base\n",
			want: "service-build",
		},
		{
			name: "include",
			body: "include:\n  - compose.shared.yml\nservices:\n  api:\n    image: example.invalid/api:latest\n",
			want: "top-level-include",
		},
		{
			name: "extends",
			body: "services:\n  api:\n    image: example.invalid/api:latest\n    extends:\n      file: compose.shared.yml\n      service: api\n",
			want: "service-extends",
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			violations, err := composePolicyViolations([]byte(fixture.body), "deploy/compose.yml")
			if err != nil {
				t.Fatalf("scan Compose fixture: %v", err)
			}
			if !slices.Contains(violations, fixture.want) {
				t.Fatalf("violations = %v, missing %s", violations, fixture.want)
			}
		})
	}
}

func TestComposePolicyFailsClosedForMultipleYAMLDocuments(t *testing.T) {
	body := "kind: workflow\nsteps: []\n---\nservices:\n  api:\n    build: .\n"
	if _, err := composePolicyViolations([]byte(body), "automation.yml"); err == nil {
		t.Fatal("Compose policy accepted a multi-document YAML stream")
	}
}
