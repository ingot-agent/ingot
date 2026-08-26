package layout

import "testing"

func TestPlatformNames(t *testing.T) {
	imageID := "sha256:0123456789abcdef"
	tests := []struct {
		goos, executable, directory string
	}{
		{goos: "linux", executable: "ingot-runtime", directory: imageID},
		{goos: "darwin", executable: "ingot-runtime", directory: imageID},
		{goos: "windows", executable: "ingot-runtime.exe", directory: "sha256-0123456789abcdef"},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			if got := RuntimeExecutableName(test.goos); got != test.executable {
				t.Fatalf("RuntimeExecutableName(%q) = %q, want %q", test.goos, got, test.executable)
			}
			if got := ImageDirectoryName(imageID, test.goos); got != test.directory {
				t.Fatalf("ImageDirectoryName(%q) = %q, want %q", test.goos, got, test.directory)
			}
			if got := ImageIDFromDirectoryName(test.directory, test.goos); got != imageID {
				t.Fatalf("ImageIDFromDirectoryName(%q) = %q, want %q", test.goos, got, imageID)
			}
		})
	}
}
