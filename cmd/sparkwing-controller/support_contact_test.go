package main

import "testing"

func TestSupportContactIsAnEmailOrAnHTTPURL(t *testing.T) {
	for contact, ok := range map[string]bool{
		"":                               true,
		"support@example.com":            true,
		"https://example.com/support":    true,
		"http://help.internal/sparkwing": true,
		"javascript:alert(1)":            false,
		"Support <support@example.com>":  false,
		"example.com":                    false,
		"https://":                       false,
	} {
		if err := validateSupportContact(contact); (err == nil) != ok {
			t.Errorf("validateSupportContact(%q) = %v, want accepted=%t", contact, err, ok)
		}
	}
}
