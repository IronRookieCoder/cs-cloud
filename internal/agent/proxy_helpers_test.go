package agent

import (
	"io"
	"strings"
	"testing"
)

func TestRenameJSONFieldAnyRenamesFirstMatchingField(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "reply field present",
			body: `{"requestID":"req-1","reply":"reject"}`,
			want: `{"requestID":"req-1","behavior":"reject"}`,
		},
		{
			name: "first candidate field present",
			body: `{"requestID":"req-1","decision":"reject"}`,
			want: `{"requestID":"req-1","behavior":"reject"}`,
		},
		{
			name: "both candidate fields present renames only first candidate",
			body: `{"requestID":"req-1","decision":"once","reply":"reject"}`,
			want: `{"requestID":"req-1","behavior":"once","reply":"reject"}`,
		},
		{
			name: "no candidate fields present",
			body: `{"requestID":"req-1","behavior":"reject"}`,
			want: `{"requestID":"req-1","behavior":"reject"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transform := RenameJSONFieldAny([]string{"decision", "reply"}, "behavior")
			body := transform(io.NopCloser(strings.NewReader(tt.body)))
			defer body.Close()

			got, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}

			if string(got) != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}
