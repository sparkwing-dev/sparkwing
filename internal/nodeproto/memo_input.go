package nodeproto

import (
	"bytes"
	"encoding/json"
	"slices"
)

// A recipe cannot smuggle fields which another host might interpret differently.
func validMemoInputFields(input MemoInput) bool {
	fields := []string{}
	if len(input.Patterns) != 0 {
		fields = append(fields, "patterns")
	}
	if input.Root != "" {
		fields = append(fields, "root")
	}
	if len(input.Names) != 0 {
		fields = append(fields, "names")
	}
	if len(input.Parts) != 0 {
		fields = append(fields, "parts")
	}
	if len(input.Inputs) != 0 {
		fields = append(fields, "inputs")
	}
	allowed := map[string]string{"files": "patterns", "repo_files": "patterns", "tree": "root", "env": "names", "parts": "parts", "compose": "inputs"}
	field, ok := allowed[input.Kind]
	return ok && (len(fields) == 0 || slices.Equal(fields, []string{field}))
}

func validateMemoInput(input MemoInput, path string) error {
	if !validMemoInputFields(input) {
		return planInvalid(path, "invalid memo input kind or fields")
	}
	if input.Kind == "tree" && input.Root == "" {
		return planInvalid(path, "tree root is required")
	}
	for _, child := range input.Inputs {
		if err := validateMemoInput(child, path+".inputs"); err != nil {
			return err
		}
	}
	return nil
}

// UnmarshalJSON preserves literal numbers through plan persistence and replay.
func (input *MemoInput) UnmarshalJSON(raw []byte) error {
	type wire MemoInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode((*wire)(input))
}
