//go:build unix

package main

import (
	"fmt"
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

// idHint states who owns a path and who this process is, in the same terms the
// kernel used when it refused the open. Printed next to a permission failure so
// the reader does not have to go and run `ls -ld` and `id` to see the mismatch
// that is already known here.
func idHint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s is uid %d gid %d mode %v; this process is uid %d gid %d",
		path, st.Uid, st.Gid, fi.Mode().Perm(), os.Geteuid(), os.Getegid())
}

// ownerOf returns path's owner as the "uid:gid" a chown command line takes, or
// "" if it cannot be read. Numeric on purpose: the account deploy.sh creates is
// named "deltascope", but a hand-rolled unit may run as anything, and a numeric
// pair is right in both cases without this having to guess a name.
func ownerOf(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Uid, st.Gid)
}
