package store

import "testing"

func TestBodyStripsOnlyTheOuterTransaction(t *testing.T) {
	for in, want := range map[string]string{
		"-- c\nBEGIN;\nCREATE TABLE x();\nCOMMIT;\n":  "-- c\n\nCREATE TABLE x();\n\n",
		"CREATE TABLE x();\n":                         "CREATE TABLE x();\n",
		"BEGIN;\nDO $$ BEGIN END $$;\nCOMMIT;":        "\nDO $$ BEGIN END $$;\n",
		"BEGIN;\nSELECT 1;\n-- trailing\nCOMMIT;\n\n": "\nSELECT 1;\n-- trailing\n\n\n",
		"BEGIN;\nSELECT 1;\n":                         "BEGIN;\nSELECT 1;\n", // no COMMIT: left alone
	} {
		if got := body(in); got != want {
			t.Errorf("body(%q) = %q, want %q", in, got, want)
		}
	}
}
