package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// isGitProject checks if the given directory is a Git project.
// It does this by checking for the existence of a .git subdirectory.
func isGitProject(dir string) bool {
	gitPath := filepath.Join(dir, ".git")
	_, err := os.Stat(gitPath)
	return err == nil
}

func main() {
	dir := flag.String("dir", ".", "Directory to check (default is current directory)")
	flag.Parse()

	if isGitProject(*dir) {
		fmt.Printf("Directory '%s' is a Git project.\n", *dir)
		os.Exit(0)
	} else {
		fmt.Printf("Directory '%s' is not a Git project.\n", *dir)
		os.Exit(1)
	}
}
