package main

import (
	"os"
	"testing"
)

// Test the simplified argument filtering logic
func TestArgumentFiltering(t *testing.T) {
	testCases := []struct {
		name                string
		args                []string
		expectedCleanArgs   []string
		expectedUnknownFlags []string
	}{
		{
			name: "only valid arguments",
			args: []string{"http://example.com/file.tar.gz"},
			expectedCleanArgs: []string{"http://example.com/file.tar.gz"},
			expectedUnknownFlags: []string{},
		},
		{
			name: "unknown flags mixed with URL",
			args: []string{"--unknown-flag", "--another-unknown", "http://example.com/file.tar.gz"},
			expectedCleanArgs: []string{"http://example.com/file.tar.gz"},
			expectedUnknownFlags: []string{"--unknown-flag", "--another-unknown"},
		},
		{
			name: "only unknown flags",
			args: []string{"--unknown-flag", "--another-unknown"},
			expectedCleanArgs: []string{},
			expectedUnknownFlags: []string{"--unknown-flag", "--another-unknown"},
		},
		{
			name: "mixed unknown flags with values and URL",
			args: []string{"--unknown-flag", "value", "--another-unknown", "http://example.com/file.tar.gz"},
			expectedCleanArgs: []string{"value", "http://example.com/file.tar.gz"},
			expectedUnknownFlags: []string{"--unknown-flag", "--another-unknown"},
		},
		{
			name: "empty args",
			args: []string{},
			expectedCleanArgs: []string{},
			expectedUnknownFlags: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Use the actual filtering function from main()
			cleanArgs, unknownFlags := filterUnknownFlags(tc.args)
			
			// Compare results
			if len(cleanArgs) != len(tc.expectedCleanArgs) {
				t.Errorf("Clean args length mismatch. Expected: %d, Got: %d", len(tc.expectedCleanArgs), len(cleanArgs))
			}
			
			for i, expected := range tc.expectedCleanArgs {
				if i >= len(cleanArgs) || cleanArgs[i] != expected {
					t.Errorf("Clean args mismatch at index %d. Expected: %s, Got: %s", i, expected, cleanArgs[i])
				}
			}
			
			if len(unknownFlags) != len(tc.expectedUnknownFlags) {
				t.Errorf("Unknown flags length mismatch. Expected: %d, Got: %d", len(tc.expectedUnknownFlags), len(unknownFlags))
			}
			
			for i, expected := range tc.expectedUnknownFlags {
				if i >= len(unknownFlags) || unknownFlags[i] != expected {
					t.Errorf("Unknown flags mismatch at index %d. Expected: %s, Got: %s", i, expected, unknownFlags[i])
				}
			}
		})
	}
}
// KServe runs a storage container as `<image> <storageUri> <destination>`, so the second
// positional argument is the output directory unless --directory says otherwise.
func TestOutputDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		flagDir string
		args    []string
		want    string
	}{
		{"URL only: current directory, as before", "", []string{"s3://b/model.tar"}, cwd},
		{"KServe: second argument", "", []string{"s3://b/model.tar", "/mnt/models"}, "/mnt/models"},
		{"--directory wins over the second argument", "/data", []string{"s3://b/model.tar", "/mnt/models"}, "/data"},
		{"--directory with URL only", "/data", []string{"s3://b/model.tar"}, "/data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := outputDir(c.flagDir, c.args); err != nil || got != c.want {
				t.Errorf("outputDir(%q, %v) = %q, %v; want %q", c.flagDir, c.args, got, err, c.want)
			}
		})
	}
}
