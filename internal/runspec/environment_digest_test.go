package runspec

import (
	"strings"
	"testing"
)

func FuzzEnvironmentDigestBindsNativeBytes(f *testing.F) {
	f.Add("VALUE", "\xff", uint8(0xfe), false)
	f.Add("N\xff", "value", uint8(0xfe), true)
	f.Add("VALUE", "one=two\nthree", uint8(0xff), false)
	f.Fuzz(func(t *testing.T, name, value string, replacement uint8, changeName bool) {
		if name == "" || value == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) || replacement == 0 || (changeName && replacement == '=') {
			t.Skip()
		}
		before, err := EnvironmentDigest(map[string]string{name: value})
		if err != nil {
			t.Fatal(err)
		}
		if changeName {
			if name[len(name)-1] == replacement {
				t.Skip()
			}
			name = name[:len(name)-1] + string([]byte{replacement})
		} else {
			if value[len(value)-1] == replacement {
				t.Skip()
			}
			value = value[:len(value)-1] + string([]byte{replacement})
		}
		after, err := EnvironmentDigest(map[string]string{name: value})
		if err != nil || before.Digest == after.Digest {
			t.Fatal("changing native bytes must change snapshot identity", err)
		}
	})
}
