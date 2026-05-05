package secrets

import "testing"

func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"API_TOKEN", false},
		{"github.token", false},
		{"aws/prod/db_password", false},
		{"my-key", false},
		{"a", false},
		{"X9._/-Y", false},

		{"", true},
		{" leading", true},
		{"trailing ", true},
		{"with space", true},
		{"new\nline", true},
		{"tab\there", true},
		{"key=value", true},
		{"key:value", true},
		{"emoji-secret", false},
		{"sparkles", false},
		{".dot-start", true},
		{"-dash-start", true},
		{"/slash-start", true},
		{"end-dash-", true},
		{"end-dot.", true},
		{"end-slash/", true},
	}
	for _, c := range cases {
		err := ValidateName(c.name)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateName(%q) err = %v, wantErr = %v", c.name, err, c.wantErr)
		}
	}

	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidateName(string(long)); err == nil {
		t.Errorf("ValidateName(257-char name) err = nil, want error")
	}
}
