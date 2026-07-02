package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteDSN_BusyTimeoutEnvOverride(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    string
		wantErr bool
	}{
		{"unset keeps default", "", "busy_timeout(30000)", false},
		{"positive integer overrides", "12345", "busy_timeout(12345)", false},
		{"non-integer errors", "lots", "", true},
		{"zero errors", "0", "", true},
		{"negative errors", "-5", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(BusyTimeoutEnvVar, tc.env)

			rw, rwErr := sqliteDSN("state.db")
			ro, roErr := sqliteReadOnlyDSN("state.db")

			if tc.wantErr {
				if rwErr == nil || roErr == nil {
					t.Fatalf("want errors, got rw=%v ro=%v", rwErr, roErr)
				}
				for _, err := range []error{rwErr, roErr} {
					if !strings.Contains(err.Error(), BusyTimeoutEnvVar) || !strings.Contains(err.Error(), tc.env) {
						t.Errorf("error %q does not name the variable and value", err)
					}
				}
				return
			}
			if rwErr != nil || roErr != nil {
				t.Fatalf("DSN errors: rw=%v ro=%v", rwErr, roErr)
			}
			if !strings.Contains(rw, tc.want) {
				t.Errorf("read-write DSN %q missing %q", rw, tc.want)
			}
			if !strings.Contains(ro, tc.want) {
				t.Errorf("read-only DSN %q missing %q", ro, tc.want)
			}
		})
	}
}

func TestOpen_InvalidBusyTimeoutFailsLoudly(t *testing.T) {
	t.Setenv(BusyTimeoutEnvVar, "soon")
	path := filepath.Join(t.TempDir(), "state.db")

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), BusyTimeoutEnvVar) {
		t.Fatalf("Open err = %v, want error naming %s", err, BusyTimeoutEnvVar)
	}
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), BusyTimeoutEnvVar) {
		t.Fatalf("OpenReadOnly err = %v, want error naming %s", err, BusyTimeoutEnvVar)
	}
}

func TestOpen_ValidBusyTimeoutOverrideOpens(t *testing.T) {
	t.Setenv(BusyTimeoutEnvVar, "1500")
	path := filepath.Join(t.TempDir(), "state.db")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open with override: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
