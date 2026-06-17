package agent

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard version with commit info",
			input:    "4.2.3 (commit: da9b1de20, built: 2026/06/16 16:09:48)",
			expected: "4.2.3",
		},
		{
			name:     "beta version",
			input:    "4.2.3-beta (commit: abc123)",
			expected: "4.2.3-beta",
		},
		{
			name:     "rc version",
			input:    "5.0.0-rc1 (built: today)",
			expected: "5.0.0-rc1",
		},
		{
			name:     "version only no suffix",
			input:    "4.2.3",
			expected: "4.2.3",
		},
		{
			name:     "version with tab separator",
			input:    "1.0.0\t(commit: abc)",
			expected: "1.0.0",
		},
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "only spaces",
			input:    "   ",
			expected: "",
		},
		{
			name:     "version with trailing newline (TrimSpace before ParseVersion)",
			input:    "3.2.1",
			expected: "3.2.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseVersion(tt.input)
			if got != tt.expected {
				t.Errorf("ParseVersion(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
