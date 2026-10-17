package envcompat

import "testing"

func TestLegacyFallback(t *testing.T) {
	env := map[string]string{"PLUMBLINE_SERVER": "https://old", "WITNESSMARK_DATA_DIR": "/new", "PLUMBLINE_DATA_DIR": "/old", "OTHER": "x"}
	get := Wrap(func(k string) string { return env[k] })
	for k, want := range map[string]string{"WITNESSMARK_SERVER": "https://old", "WITNESSMARK_DATA_DIR": "/new", "WITNESSMARK_NONE": "", "OTHER": "x"} {
		if got := get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
