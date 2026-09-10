package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUsersAddReadsPasswordLine(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		invalid           bool
	}{
		{"passphrase", "my pass word\n", "my pass word", false},
		{"padding", "  my pass word  \n", "  my pass word  ", false},
		{"CRLF", "my pass word\r\n", "my pass word", false},
		{"EOF", "my pass word", "my pass word", false},
		{"blank", "\n", "", true},
		{"short", "short\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received string
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var body struct {
					Password string `json:"password"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				received = body.Password
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("{}"))
			}))
			defer srv.Close()
			writeProfilesFixture(t, "profiles:\n  test:\n    controller: {url: "+srv.URL+"}\n")
			withStdin(t, tc.input)
			err := runUsersAdd([]string{"--name", "fixture", "--profile", "test"})
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "at least 8") {
					t.Fatalf("error = %v", err)
				}
				if calls != 0 {
					t.Fatal("invalid password reached controller")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 || received != tc.want {
					t.Fatalf("password line not preserved: calls=%d got=%q", calls, received)
				}
			}
		})
	}
}
