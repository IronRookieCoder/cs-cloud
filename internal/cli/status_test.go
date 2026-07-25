package cli

import (
	"os"
	"testing"
)

func TestParseStatusJSONFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"cs-cloud", "status"}, false},
		{[]string{"cs-cloud", "status", "--json"}, true},
		{[]string{"cs-cloud", "--json", "status"}, true},
		{[]string{"cs-cloud", "status", "--json=true"}, true},
		{[]string{"cs-cloud", "status", "--json=false"}, false},
		{[]string{"cs-cloud", "status", "--json=1"}, true},
		{[]string{"cs-cloud", "status", "--json=0"}, false},
		{[]string{"cs-cloud", "status", "--json="}, false},
	}
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	for _, c := range cases {
		os.Args = c.args
		if got := parseStatusJSONFlag(); got != c.want {
			t.Errorf("args=%v: want %v, got %v", c.args, c.want, got)
		}
	}
}
