package localstore

import (
	"testing"
)

func TestSanitizeObjectName(t *testing.T) {
	_, err := sanitizeObjectName("../evil.jpg")
	if err == nil {
		t.Fatalf("expected error for path traversal")
	}

	name, err := sanitizeObjectName("crops/job.jpg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "crops/job.jpg" {
		t.Fatalf("unexpected name: %s", name)
	}
}

func TestEscapePath(t *testing.T) {
	escaped, err := escapePath("crops/space name.jpg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if escaped != "crops/space%20name.jpg" {
		t.Fatalf("unexpected escaped path: %s", escaped)
	}
}
