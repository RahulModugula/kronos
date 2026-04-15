package replay

import (
	"encoding/json"
	"fmt"
	"strings"
)

type DiffResult struct {
	Lines     []string
	DiffBytes int
}

func DiffJSON(a, b json.RawMessage) (*DiffResult, error) {
	if a == nil && b == nil {
		return &DiffResult{}, nil
	}

	var aVal, bVal interface{}
	if a != nil {
		if err := json.Unmarshal(a, &aVal); err != nil {
			return nil, fmt.Errorf("unmarshal a: %w", err)
		}
	}
	if b != nil {
		if err := json.Unmarshal(b, &bVal); err != nil {
			return nil, fmt.Errorf("unmarshal b: %w", err)
		}
	}

	aPretty, _ := json.MarshalIndent(aVal, "", "  ")
	bPretty, _ := json.MarshalIndent(bVal, "", "  ")

	aLines := strings.Split(string(aPretty), "\n")
	bLines := strings.Split(string(bPretty), "\n")

	maxLen := len(aLines)
	if len(bLines) > maxLen {
		maxLen = len(bLines)
	}

	var diffLines []string
	diffBytes := 0

	for i := 0; i < maxLen; i++ {
		aLine := ""
		bLine := ""
		if i < len(aLines) {
			aLine = aLines[i]
		}
		if i < len(bLines) {
			bLine = bLines[i]
		}

		if aLine == bLine {
			diffLines = append(diffLines, "  "+aLine)
		} else {
			if aLine != "" {
				diffLines = append(diffLines, "- "+aLine)
				diffBytes += len(aLine)
			}
			if bLine != "" {
				diffLines = append(diffLines, "+ "+bLine)
				diffBytes += len(bLine)
			}
		}
	}

	return &DiffResult{Lines: diffLines, DiffBytes: diffBytes}, nil
}
