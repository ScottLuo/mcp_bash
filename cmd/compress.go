package bash

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// excludedDirPatterns are directory names that are excluded from path-list
// compression. These directories usually hold many files that do not help an
// LLM understand a project's structure.
var excludedDirPatterns = []string{
	".git",
	".github",
	"node_modules",
	"vendor",
	".idea",
	".vscode",
	"__pycache__",
	".cache",
}

// isExcludedPath reports whether any segment of path matches an excluded
// directory pattern.
func isExcludedPath(path string) bool {
	segments := strings.Split(path, "/")
	for _, seg := range segments {
		for _, pattern := range excludedDirPatterns {
			if seg == pattern {
				return true
			}
		}
	}
	return false
}

// AutoCompressShellOutput auto-detects the type of shell output and applies
// the best compression strategy. Three main formats are supported:
//  1. go test -v format: "=== RUN / --- PASS / --- FAIL" -> merge PASS subtests,
//     keep FAIL details.
//  2. grep/rg format: "path:lineNo:content" -> group by file, merge identical
//     content lines.
//  3. find/ls format: "path" -> extract a common directory prefix.
//
// If the compressed output is not shorter than the original, the original is
// returned unchanged.
func AutoCompressShellOutput(rawOutput string) string {
	if rawOutput == "" {
		return rawOutput
	}

	lines := strings.Split(rawOutput, "\n")
	nonEmptyLines := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmptyLines = append(nonEmptyLines, l)
		}
	}

	if len(nonEmptyLines) == 0 {
		return rawOutput
	}

	// Detect go test -v format: whether more than 2 of the first 10 lines match
	// "=== RUN" or "--- (PASS|FAIL|SKIP):".
	isGoTestFormat := false
	goTestCount := 0
	checkLimit := 10
	if len(nonEmptyLines) < checkLimit {
		checkLimit = len(nonEmptyLines)
	}
	for i := 0; i < checkLimit; i++ {
		line := nonEmptyLines[i]
		if strings.HasPrefix(line, "=== RUN") ||
			strings.HasPrefix(line, "--- PASS:") ||
			strings.HasPrefix(line, "--- FAIL:") ||
			strings.HasPrefix(line, "--- SKIP:") {
			goTestCount++
		}
	}
	if goTestCount >= 2 {
		isGoTestFormat = true
	}

	if isGoTestFormat {
		return compressGoTestFormat(rawOutput)
	}

	// Detect grep/rg format: heuristic — if the majority of the first 10 lines
	// match "X:Y:Z" where Y is a number, treat it as grep output.
	isGrepFormat := false
	grepCount := 0
	for i := 0; i < checkLimit; i++ {
		line := nonEmptyLines[i]
		colon1 := strings.Index(line, ":")
		if colon1 == -1 {
			continue
		}
		rest := line[colon1+1:]
		colon2 := strings.Index(rest, ":")
		if colon2 == -1 {
			continue
		}
		lineNoStr := rest[:colon2]
		if _, err := strconv.Atoi(lineNoStr); err == nil {
			grepCount++
		}
	}
	// Consider it grep format if more than half of the checked lines match.
	if grepCount > len(nonEmptyLines)/2 && grepCount > 0 {
		isGrepFormat = true
	}

	if isGrepFormat {
		return compressGrepFormat(rawOutput)
	}
	return compressPathListFormat(rawOutput)
}

// compressGrepFormat processes grep/rg output (path:line:content), grouping
// lines by file and merging identical content across line numbers.
func compressGrepFormat(rawOutput string) string {
	lines := strings.Split(rawOutput, "\n")
	fileMap := make(map[string]map[string][]int)
	excludedCount := 0

	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue
		}

		colon1 := strings.Index(line, ":")
		if colon1 == -1 {
			continue
		}
		pathPart := line[:colon1]
		// Skip paths under excluded directories.
		if isExcludedPath(pathPart) {
			excludedCount++
			continue
		}
		rest := line[colon1+1:]

		colon2 := strings.Index(rest, ":")
		if colon2 == -1 {
			continue
		}

		lineNoStr := rest[:colon2]
		contentPart := rest[colon2+1:]

		lineNo, err := strconv.Atoi(lineNoStr)
		if err != nil {
			continue
		}

		// Normalize content (trim) to merge duplicates.
		normalizedContent := strings.TrimSpace(contentPart)
		if normalizedContent == "" {
			normalizedContent = contentPart
		}

		if _, ok := fileMap[pathPart]; !ok {
			fileMap[pathPart] = make(map[string][]int)
		}
		fileMap[pathPart][normalizedContent] = append(fileMap[pathPart][normalizedContent], lineNo)
	}

	if len(fileMap) == 0 {
		if excludedCount > 0 {
			return fmt.Sprintf("[All %d lines are in excluded directories and have been filtered out.]", excludedCount)
		}
		return rawOutput
	}

	var builder strings.Builder
	originalLen := len(rawOutput)

	filePaths := make([]string, 0, len(fileMap))
	for fp := range fileMap {
		filePaths = append(filePaths, fp)
	}
	sort.Strings(filePaths)

	for _, path := range filePaths {
		contentMap := fileMap[path]
		fmt.Fprintf(&builder, "[File: %s]\n", path)

		contents := make([]string, 0, len(contentMap))
		for c := range contentMap {
			contents = append(contents, c)
		}
		// Sort content for stable output.
		sort.Strings(contents)

		for _, content := range contents {
			nums := contentMap[content]
			sort.Ints(nums)

			if len(nums) == 1 {
				fmt.Fprintf(&builder, "%d: %s\n", nums[0], content)
			} else {
				numStrs := make([]string, len(nums))
				for i, n := range nums {
					numStrs[i] = strconv.Itoa(n)
				}
				joinedNums := strings.Join(numStrs, ",")
				fmt.Fprintf(&builder, "%s: %s\n", joinedNums, content)
			}
		}
		builder.WriteString("\n")
	}

	result := builder.String()
	if len(result) < originalLen {
		return result
	}
	return rawOutput
}

// compressPathListFormat processes find/ls output (a list of paths).
// Strategy: 1) extract the longest common directory prefix, 2) group by the
// immediate parent directory and merge files in the same directory.
func compressPathListFormat(rawOutput string) string {
	lines := strings.Split(rawOutput, "\n")
	paths := make([]string, 0, len(lines))
	var nonPathLines []string
	excludedPathCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Drop paths under excluded directories outright.
		if isExcludedPath(trimmed) {
			excludedPathCount++
			continue
		}
		// Only lines containing "/" are treated as path candidates; other lines
		// are kept as non-path lines.
		if strings.Contains(trimmed, "/") {
			paths = append(paths, trimmed)
		} else {
			nonPathLines = append(nonPathLines, trimmed)
		}
	}

	if len(paths) == 0 {
		if excludedPathCount > 0 {
			return fmt.Sprintf("[All %d path lines are in excluded directories and have been filtered out.]", excludedPathCount)
		}
		return rawOutput
	}

	// A single path cannot be compressed.
	if len(paths) == 1 {
		return rawOutput
	}

	// Compute the longest common directory prefix.
	prefix := findLongestCommonDirPrefix(paths)

	// If the prefix is too short (e.g. "/" or empty), do not compress.
	if len(prefix) < 2 || prefix == "/" || prefix == "" {
		return rawOutput
	}

	originalLen := len(rawOutput)

	// Group by immediate parent directory.
	type dirGroup struct {
		files []string
	}
	groups := make(map[string]*dirGroup)
	var dirOrder []string

	for _, path := range paths {
		var relPath string
		if strings.HasPrefix(path, prefix) {
			relPath = path[len(prefix):]
		} else {
			relPath = path
		}

		if relPath == "" {
			relPath = " (self)"
		}

		lastSlash := strings.LastIndex(relPath, "/")
		if lastSlash == -1 {
			// No leading directory; group under the root bucket.
			if _, ok := groups[""]; !ok {
				groups[""] = &dirGroup{}
				dirOrder = append(dirOrder, "")
			}
			groups[""].files = append(groups[""].files, relPath)
		} else {
			dir := relPath[:lastSlash+1] // include the trailing '/'
			file := relPath[lastSlash+1:]
			if _, ok := groups[dir]; !ok {
				groups[dir] = &dirGroup{}
				dirOrder = append(dirOrder, dir)
			}
			groups[dir].files = append(groups[dir].files, file)
		}
	}

	// Sort directories for stable output.
	sort.Strings(dirOrder)

	var builder strings.Builder
	builder.Grow(originalLen / 2)

	fmt.Fprintf(&builder, "[Context Path: %s]\n", prefix)

	for _, dir := range dirOrder {
		files := groups[dir].files
		if dir == "" {
			// Files directly under the root: print file names.
			builder.WriteString(strings.Join(files, ", ") + "\n")
		} else {
			// Files under a subdirectory: "dirname/: file1, file2, ...".
			fmt.Fprintf(&builder, "%s: %s\n", dir, strings.Join(files, ", "))
		}
	}

	// Append non-path lines (e.g. "---", short file names).
	if len(nonPathLines) > 0 {
		builder.WriteString(strings.Join(nonPathLines, "\n"))
		builder.WriteString("\n")
	}

	result := builder.String()

	// Verify that the compression actually produced a shorter result.
	if len(result) < originalLen {
		return result
	}

	return rawOutput
}

// compressGoTestFormat processes go test -v output.
// Compression strategy:
//   - All-PASS tests: merge into "PASS: TestName (N subtests)".
//   - Tests with FAIL: keep the FAIL details (indented output lines and
//     --- FAIL lines), compress PASS subtests.
//   - Keep the final PASS/FAIL and ok/FAIL summary lines.
//   - If the compressed output is not shorter than the original, return the
//     original output.
func compressGoTestFormat(rawOutput string) string {
	lines := strings.Split(rawOutput, "\n")

	// topTest records information about a top-level test.
	type topTest struct {
		name         string
		subtestCount int
		status       string // "PASS", "FAIL", "SKIP"
		startLine    int // index of the === RUN line
		endLine      int // index of the top-level test result line
	}

	var tests []topTest
	var packageLines []string

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Package-level result line: "PASS" or "FAIL" (on its own).
		if trimmed == "PASS" || trimmed == "FAIL" {
			packageLines = append(packageLines, line)
			continue
		}

		// Package-level result line: "ok\tpkg\ttime" or "FAIL\tpkg\ttime".
		if (strings.HasPrefix(line, "ok\t") || strings.HasPrefix(line, "ok  ") ||
			strings.HasPrefix(line, "FAIL\t") || strings.HasPrefix(line, "FAIL  ")) &&
			!strings.HasPrefix(line, "--- ") {
			packageLines = append(packageLines, line)
			continue
		}

		// === RUN   TestName line.
		if strings.HasPrefix(line, "=== RUN   ") {
			testName := strings.TrimPrefix(line, "=== RUN   ")
			if !strings.Contains(testName, "/") {
				// New top-level test.
				tests = append(tests, topTest{name: testName, startLine: i})
			} else {
				// Subtest; count it.
				if len(tests) > 0 {
					tests[len(tests)-1].subtestCount++
				}
			}
			continue
		}

		// --- PASS/FAIL/SKIP: TestName (time) line.
		if strings.HasPrefix(line, "--- PASS:") || strings.HasPrefix(line, "--- FAIL:") ||
			strings.HasPrefix(line, "--- SKIP:") {
			var status string
			switch {
			case strings.HasPrefix(line, "--- PASS:"):
				status = "PASS"
			case strings.HasPrefix(line, "--- FAIL:"):
				status = "FAIL"
			default:
				status = "SKIP"
			}

			// Extract the test name (drop the prefix and the "(time)" suffix).
			var testName string
			switch {
			case strings.HasPrefix(line, "--- PASS:"):
				testName = strings.TrimPrefix(line, "--- PASS:")
			case strings.HasPrefix(line, "--- FAIL:"):
				testName = strings.TrimPrefix(line, "--- FAIL:")
			default:
				testName = strings.TrimPrefix(line, "--- SKIP:")
			}
			// Remove the trailing " (0.00s)" part.
			if idx := strings.LastIndex(testName, " ("); idx != -1 {
				testName = testName[:idx]
			}
			testName = strings.TrimSpace(testName)

			// Top-level test result.
			if !strings.Contains(testName, "/") && len(tests) > 0 &&
				tests[len(tests)-1].name == testName {
				tests[len(tests)-1].status = status
				tests[len(tests)-1].endLine = i
				// Extend endLine to include subsequent indented subtest result
				// lines.
				for k := i + 1; k < len(lines); k++ {
					nextLine := lines[k]
					if strings.HasPrefix(nextLine, "    --- PASS:") ||
						strings.HasPrefix(nextLine, "    --- FAIL:") ||
						strings.HasPrefix(nextLine, "    --- SKIP:") {
						tests[len(tests)-1].endLine = k
						continue
					}
					break
				}
			}
			continue
		}

		// Other lines (test output, blank lines, etc.) - not handled separately.
	}

	// Build the compressed output.
	var builder strings.Builder
	builder.Grow(len(rawOutput) / 4)

	for _, test := range tests {
		switch test.status {
		case "FAIL":
			// Keep the detailed output for failing tests.
			fmt.Fprintf(&builder, "FAIL: %s\n", test.name)
			for j := test.startLine; j <= test.endLine && j < len(lines); j++ {
				l := lines[j]
				trimmedL := strings.TrimSpace(l)
				// Skip === RUN lines (already replaced by "FAIL: name").
				if strings.HasPrefix(trimmedL, "=== RUN") {
					continue
				}
				// Skip PASS subtest lines (indented or not).
				if strings.HasPrefix(trimmedL, "--- PASS:") {
					continue
				}
				// Keep FAIL and SKIP lines (indented or not).
				if strings.HasPrefix(trimmedL, "--- FAIL:") || strings.HasPrefix(trimmedL, "--- SKIP:") {
					builder.WriteString(l)
					builder.WriteByte('\n')
					continue
				}
				// Keep non-empty lines (test output, error messages).
				if trimmedL != "" {
					builder.WriteString(l)
					builder.WriteByte('\n')
				}
			}
			builder.WriteByte('\n')
		case "SKIP":
			if test.subtestCount > 0 {
				fmt.Fprintf(&builder, "SKIP: %s (%d subtests)\n", test.name, test.subtestCount)
			} else {
				fmt.Fprintf(&builder, "SKIP: %s\n", test.name)
			}
		default: // PASS
			if test.subtestCount > 0 {
				fmt.Fprintf(&builder, "PASS: %s (%d subtests)\n", test.name, test.subtestCount)
			} else {
				fmt.Fprintf(&builder, "PASS: %s\n", test.name)
			}
		}
	}

	// Append the package-level summary lines.
	for _, pl := range packageLines {
		builder.WriteString(pl)
		builder.WriteByte('\n')
	}

	result := strings.TrimRight(builder.String(), "\n")

	// Only return the compressed result if it is actually shorter.
	if len(result) < len(rawOutput) {
		return result
	}
	return rawOutput
}

// findLongestCommonDirPrefix finds the longest common directory prefix of a
// set of paths, ensuring the prefix ends with '/' so file names split
// correctly.
func findLongestCommonDirPrefix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}

	// Initialize LCP with the first path.
	lcp := paths[0]

	for i := 1; i < len(paths); i++ {
		lcp = commonPrefix(lcp, paths[i])
		if lcp == "" {
			break
		}
	}

	// If the LCP is empty, return immediately.
	if lcp == "" {
		return ""
	}

	lastSlash := strings.LastIndex(lcp, "/")

	// Case 1: LCP ends with '/'.
	if strings.HasSuffix(lcp, "/") {
		return lcp
	}

	// Case 2: LCP does not end with '/'; the common part extends into the file
	// name. Truncate back to the last '/' so the result is a directory prefix.
	if lastSlash != -1 {
		if lastSlash > 0 {
			return lcp[:lastSlash+1]
		}
	}

	// No valid directory prefix.
	return ""
}

// commonPrefix returns the longest common prefix of two strings.
func commonPrefix(a, b string) string {
	maxLen := len(a)
	if len(b) < maxLen {
		maxLen = len(b)
	}

	i := 0
	for i < maxLen && a[i] == b[i] {
		i++
	}

	return a[:i]
}
