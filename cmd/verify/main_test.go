package main

import (
	"bytes"
	"testing"
)

func TestRunPrintsVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "default build", version: "dev", want: "plumbline-verify dev\n"},
		{name: "release build", version: "v0.1.0", want: "plumbline-verify v0.1.0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := run(&out, tt.version); code != 0 {
				t.Fatalf("run exit code = %d, want 0", code)
			}
			if got := out.String(); got != tt.want {
				t.Errorf("run output = %q, want %q", got, tt.want)
			}
		})
	}
}
