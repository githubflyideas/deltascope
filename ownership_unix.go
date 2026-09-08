//go:build unix

package main

import (
	"log"
	"os"
	"syscall"
)

// alignDataOwnership hands the database files back to whoever owns the data
// directory, when we are root and they are not.
//
// This exists because of one specific way to lock yourself out. SQLite in WAL
// mode keeps two sidecar files next to the database, and opening the database
// -- even just to read it -- requires write access to the -shm file. So a
// single `sudo deltascope user del admin` against a data directory owned by
// the service account leaves root-owned files behind, and from that moment
// the service cannot read its own user table. The account is still there and
// the password is still right, but every login fails, and the only clue is a
// permission error the operator never sees. Following the directory's owner
// keeps the CLI and the service able to share one database.
func alignDataOwnership(dataDir string, files ...string) {
	if os.Geteuid() != 0 {
		return
	}
	di, err := os.Stat(dataDir)
	if err != nil {
		return
	}
	dst, ok := di.Sys().(*syscall.Stat_t)
	if !ok || (dst.Uid == 0 && dst.Gid == 0) {
		return // root's own directory; nothing to hand back
	}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			continue // -wal and -shm exist only while the database is open
		}
		if fst, ok := fi.Sys().(*syscall.Stat_t); ok && fst.Uid == dst.Uid && fst.Gid == dst.Gid {
			continue
		}
		if err := os.Chown(f, int(dst.Uid), int(dst.Gid)); err != nil {
			log.Printf("warning: %s stays owned by root (%v); the deltascope service may not be able to open it", f, err)
			continue
		}
		log.Printf("gave %s back to uid %d:%d, matching %s", f, dst.Uid, dst.Gid, dataDir)
	}
}
