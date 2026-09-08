//go:build !unix

package main

// alignDataOwnership is a no-op off unix. The problem it solves is a
// root-CLI-versus-service-account split created by systemd, and there is no
// uid to follow here; this file exists so the package still builds for
// contributors developing on Windows.
func alignDataOwnership(dataDir string, files ...string) {}
