package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cover the preflight check on the data directory, and they exist because
// of one real report. Running the release binary by hand on a host that had
// already been deployed printed:
//
//	failed to open SQLite: failed to init schema: unable to open database file: out of memory (14)
//
// Nothing was out of memory. 14 is SQLITE_CANTOPEN; the driver prints
// sqlite3_errstr(rc) and sqlite3_errmsg(db) side by side and the second is
// meaningless when the handle never opened, so it contributed the words "out of
// memory" to a permissions failure. The directory belonged to the service
// account and the shell did not.
//
// So the assertion throughout is on the message. An exit code proves the check
// fired; only the text proves it told the reader what to do.

func TestDataGuardAcceptsADirectoryItCanWrite(t *testing.T) {
	dir := t.TempDir()
	if err := checkDataUsable(dir, filepath.Join(dir, "deltascope.db")); err != nil {
		t.Fatalf("rejected a writable directory: %v", err)
	}
	// The probe writes a file to find out whether it can; leaving it behind
	// would litter every data directory with one more entry per restart.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".deltascope-probe-") {
			t.Errorf("probe file %s was left behind", e.Name())
		}
	}
}

// A missing database is the first run, and a missing -wal/-shm pair is simply a
// database that is not open. Neither is a problem, and treating either as one
// would break every first start.
func TestDataGuardIsQuietAboutAnAbsentDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "deltascope.db")
	if err := os.WriteFile(db, []byte("not really sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	// db exists, sidecars do not -- the state of every cleanly-stopped install.
	if err := checkDataUsable(dir, db); err != nil {
		t.Fatalf("complained about absent sidecars: %v", err)
	}
}

// The case from the report, one layer in: the directory is writable but the
// database itself is not. This is what a run as root leaves behind inside a
// directory owned by the service account, and it is the shape that makes the
// service fail after a CLI command appeared to work.
func TestDataGuardRefusesADatabaseItCannotOpen(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "deltascope.db")
	if err := os.WriteFile(db, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(db, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(db, 0o600) })

	// Root ignores the mode bits, and so does any host where the read-only
	// attribute does not deny an O_RDWR open. Where the open still succeeds
	// there is nothing for the check to catch, and asserting it failed would be
	// asserting something about the platform instead of about this code.
	if fh, err := os.OpenFile(db, os.O_RDWR, 0); err == nil {
		fh.Close()
		t.Skip("this host lets us open a 0400 file read-write; nothing to detect")
	}

	err := checkDataUsable(dir, db)
	if err == nil {
		t.Fatal("accepted a database it cannot open read-write")
	}
	msg := err.Error()
	for _, want := range []string{db, "read-write", "previous run as root"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
	// The repair command can only be spelled out where the directory's owner is
	// readable, which is unix. Asserting it unconditionally would be asserting
	// that this test runs on Linux, and it has to pass on a Windows checkout too.
	if ownerOf(dir) != "" && !strings.Contains(msg, "chown") {
		t.Errorf("owner is known but no chown command was offered:\n%s", msg)
	}
	if strings.Contains(msg, "out of memory") {
		t.Errorf("message repeats SQLite's bogus out-of-memory text:\n%s", msg)
	}
	// A format verb that ran out of arguments prints "%!s(MISSING)", which is how
	// a message assembled from optional facts goes wrong.
	if strings.Contains(msg, "%!") {
		t.Errorf("message has a formatting artifact:\n%s", msg)
	}
}

// When the directory cannot be written at all, the message has to carry the way
// out. Whichever errno the host produces, the three routes -- run as the owner,
// run as root, or pick a directory you own -- are what the reader needs, and
// -data is the flag they have to know about.
func TestDataGuardNamesTheFixWhenTheDirectoryIsUnusable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	err := checkDataUsable(missing, filepath.Join(missing, "deltascope.db"))
	if err == nil {
		t.Fatal("accepted a directory that cannot be written")
	}
	msg := err.Error()
	for _, want := range []string{missing, "-data", "sudo"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "out of memory") {
		t.Errorf("message repeats SQLite's bogus out-of-memory text:\n%s", msg)
	}
	if strings.Contains(msg, "%!") {
		t.Errorf("message has a formatting artifact:\n%s", msg)
	}
	// The uid route is offered only when the owner could be read. When it cannot,
	// the line must be absent rather than present with a hole in it: `sudo -u '#'`
	// is a command that cannot run, printed where the fix should be.
	if ownerOf(missing) == "" && strings.Contains(msg, "-u '#'") {
		t.Errorf("offered a sudo -u with no uid in it:\n%s", msg)
	}
}
