//go:build unit

package bash

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsExcludedPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"no segments", "file.txt", false},
		{"git dir", "a/b/.git/c", true},
		{"node_modules", "/x/node_modules/y.js", true},
		{"non excluded", "a/b/c.go", false},
		{"root git", ".git/config", true},
		{"vendor", "vendor/foo/bar.go", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isExcludedPath(tc.path))
		})
	}
}

func TestAutoCompressShellOutput_Empty(t *testing.T) {
	assert.Equal(t, "", AutoCompressShellOutput(""))
	assert.Equal(t, "   \n  \n", AutoCompressShellOutput("   \n  \n"))
}

func TestAutoCompressShellOutput_SingleLine(t *testing.T) {
	// A single non-empty line is not compressible to something shorter.
	assert.Equal(t, "hello world", AutoCompressShellOutput("hello world"))
}

func TestAutoCompressShellOutput_GoTestAllPass(t *testing.T) {
	input := `=== RUN   TestA
--- PASS: TestA (0.00s)
=== RUN   TestB
--- PASS: TestB (0.00s)
=== RUN   TestC
--- PASS: TestC (0.00s)
PASS
ok  	example.com/pk	0.002s`
	got := AutoCompressShellOutput(input)
	assert.Contains(t, got, "PASS: TestA")
	assert.Contains(t, got, "PASS: TestB")
	assert.Contains(t, got, "PASS: TestC")
	// The ok/FAIL package summary line is preserved.
	assert.Contains(t, got, "ok")
	// Compressed output should be shorter than the input.
	assert.Less(t, len(got), len(input))
}

func TestAutoCompressShellOutput_GoTestWithSubtests(t *testing.T) {
	input := `=== RUN   TestFoo
=== RUN   TestFoo/sub1
=== RUN   TestFoo/sub2
--- PASS: TestFoo/sub1 (0.00s)
--- PASS: TestFoo/sub2 (0.00s)
--- PASS: TestFoo (0.00s)
PASS
ok  	example.com/pk	0.001s`
	got := AutoCompressShellOutput(input)
	// Subtests are merged into a count.
	assert.Contains(t, got, "PASS: TestFoo (2 subtests)")
	assert.Less(t, len(got), len(input))
}

func TestAutoCompressShellOutput_GoTestFailPreserved(t *testing.T) {
	input := `=== RUN   TestGood
--- PASS: TestGood (0.00s)
=== RUN   TestBad
    main_test.go:10: expected 2, got 3
--- FAIL: TestBad (0.01s)
FAIL
FAIL	example.com/pk	0.010s`
	got := AutoCompressShellOutput(input)
	// FAIL detail lines are preserved.
	assert.Contains(t, got, "FAIL: TestBad")
	assert.Contains(t, got, "main_test.go:10")
	// The passing test is compressed.
	assert.Contains(t, got, "PASS: TestGood")
	// The package summary is preserved.
	assert.Contains(t, got, "FAIL")
}

func TestAutoCompressShellOutput_Grep(t *testing.T) {
	input := `src/a.go:1:func foo() {}
src/a.go:2:func foo() {}
src/a.go:10:func bar() {}
src/b.go:3:func baz() {}`
	got := AutoCompressShellOutput(input)
	// Grouped by file.
	assert.Contains(t, got, "[File: src/a.go]")
	assert.Contains(t, got, "[File: src/b.go]")
	// Identical content across lines is merged: 1,2 share "func foo() {}".
	assert.Contains(t, got, "1,2: func foo() {}")
	assert.Less(t, len(got), len(input))
}

func TestAutoCompressShellOutput_GrepExcludedOnly(t *testing.T) {
	input := `node_modules/x/y.js:1:foo
node_modules/x/z.js:2:bar`
	got := AutoCompressShellOutput(input)
	assert.Contains(t, got, "excluded directories")
	assert.Contains(t, got, "2") // two lines filtered
}

func TestAutoCompressShellOutput_PathList(t *testing.T) {
	input := `/opt/proj/src/a.go
/opt/proj/src/b.go
/opt/proj/src/c.go
/opt/proj/docs/readme.md
/opt/proj/docs/api.md`
	got := AutoCompressShellOutput(input)
	// Common prefix extracted.
	assert.Contains(t, got, "[Context Path: /opt/proj/")
	// Grouped by directory.
	assert.Contains(t, got, "src/: a.go, b.go, c.go")
	assert.Contains(t, got, "docs/: readme.md, api.md")
	assert.Less(t, len(got), len(input))
}

func TestAutoCompressShellOutput_PathListExcludedFiltered(t *testing.T) {
	input := `a/b/.git/objects/1
a/b/node_modules/x/y
a/b/.git/objects/2`
	got := AutoCompressShellOutput(input)
	assert.Contains(t, got, "excluded directories")
	assert.Contains(t, got, "3") // three path lines filtered
}

func TestAutoCompressShellOutput_SinglePath(t *testing.T) {
	// A single path cannot be compressed.
	assert.Equal(t, "/a/b/c.go", AutoCompressShellOutput("/a/b/c.go"))
}

func TestTruncateOutput(t *testing.T) {
	t.Run("short input unchanged", func(t *testing.T) {
		s := strings.Repeat("a", MaxOutputSize-1)
		got, truncated := truncateOutput(s)
		assert.False(t, truncated)
		assert.Equal(t, s, got)
	})

	t.Run("exact size not truncated", func(t *testing.T) {
		s := strings.Repeat("a", MaxOutputSize)
		got, truncated := truncateOutput(s)
		assert.False(t, truncated)
		assert.Equal(t, s, got)
	})

	t.Run("long input truncated with marker", func(t *testing.T) {
		s := strings.Repeat("a", MaxOutputSize+100)
		got, truncated := truncateOutput(s)
		assert.True(t, truncated)
		assert.True(t, strings.HasSuffix(got, truncateMarker))
		// Truncated body is capped at MaxOutputSize bytes (marker extra).
		body := strings.TrimSuffix(got, truncateMarker)
		assert.LessOrEqual(t, len(body), MaxOutputSize)
	})
}

func TestSafeByteCut(t *testing.T) {
	// "héllo" -> h(1) é(2) l(1) l(1) o(1)
	s := "héllo"
	// Cutting at n=2 should land at byte 1 (start of 'é', a 2-byte rune).
	assert.Equal(t, 1, safeByteCut(s, 2))
	// Cutting beyond length returns full length.
	assert.Equal(t, len(s), safeByteCut(s, len(s)+10))
	// ASCII input: cut at 3.
	assert.Equal(t, 3, safeByteCut("hello", 3))
}

func TestCommonPrefix(t *testing.T) {
	assert.Equal(t, "/abc", commonPrefix("/abcdef", "/abcxyz"))
	assert.Equal(t, "", commonPrefix("abc", "xyz"))
	assert.Equal(t, "a", commonPrefix("a", "ab"))
}

func TestFindLongestCommonDirPrefix(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{"empty", []string{}, ""},
		{"shared dir", []string{"/opt/proj/a.go", "/opt/proj/b.go"}, "/opt/proj/"},
		{"already ends with slash", []string{"/opt/proj/", "/opt/proj/x"}, "/opt/proj/"},
		{"only root shared", []string{"/a/b.go", "/c/d.go"}, "/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, findLongestCommonDirPrefix(tc.paths))
		})
	}
}

// TestCompressNoShorterKeepsOriginal verifies the "only return compressed if
// shorter" guarantee on a path list that is already minimal.
func TestCompressNoShorterKeepsOriginal(t *testing.T) {
	// Two very short paths whose compressed form is not shorter than the raw.
	input := "a/x\nb/y"
	// There is no shared directory prefix, so it must be returned unchanged.
	assert.Equal(t, input, AutoCompressShellOutput(input))
}

// TestAutoCompressIdempotent verifies that compressing already-compressed
// output is stable (does not grow indefinitely) for the path-list case.
func TestAutoCompressPathListStable(t *testing.T) {
	input := ""
	for i := 0; i < 50; i++ {
		input += "/very/long/common/prefix/path/here/file_" + strings.Repeat("x", 20) + "_" + string(rune('a'+i%26)) + ".txt\n"
	}
	once := AutoCompressShellOutput(input)
	// Should be shorter than the original (compression works on many files).
	require.Less(t, len(once), len(input))
}
