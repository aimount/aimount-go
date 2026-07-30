package agent

import (
	"go/build"
	"strings"
	"testing"
)

func TestAgentPackageUsesOnlyStandardLibrary(t *testing.T) {
	pkg, err := build.Default.Import("github.com/aimount/aimount-go/agent", "", build.FindOnly)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := build.ImportDir(pkg.Dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range parsed.Imports {
		if strings.Contains(path, ".") {
			t.Fatalf("agent imports non-standard package %q", path)
		}
	}
}
