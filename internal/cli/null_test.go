package cli

import "testing"

func TestOpenNullDeviceSupportsChildOutput(t *testing.T) {
	file, err := openNullDevice()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("daemon output")); err != nil {
		t.Fatalf("write null device: %v", err)
	}
}
