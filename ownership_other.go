//go:build !unix

package main

// alignDataOwnership is a no-op off unix. The problem it solves is a
// root-CLI-versus-service-account split created by systemd, and there is no
// uid to follow here; this file exists so the package still builds for
// contributors developing on Windows.
func alignDataOwnership(dataDir string, files ...string) {}

// idHint and ownerOf report nothing off unix. The permission check that calls
// them still runs -- it probes by creating a file, which works anywhere -- it
// just cannot add the uid/gid line that makes the failure obvious on the host
// where this actually bites.
func idHint(path string) string  { return "" }
func ownerOf(path string) string { return "" }
