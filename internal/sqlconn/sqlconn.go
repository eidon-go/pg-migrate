// Package sqlconn holds the handling of a dedicated *sql.Conn that both the
// advisory lock and the notransaction execution path need. It exists so the two
// do not each carry their own copy of logic that must not drift: getting it
// wrong strands a lock or leaks session state into the caller's pool.
package sqlconn

import (
	"database/sql"
	"database/sql/driver"
	"io"
)

// Destroy terminates the underlying session instead of returning the connection
// to the pool.
//
// This is the only way to be certain a connection carries nothing forward.
// (*sql.Conn).Close merely returns it to the pool with its session — and so its
// GUCs, temporary tables and session-level advisory locks — intact. Use it
// whenever the connection's state cannot be vouched for.
func Destroy(conn *sql.Conn) {
	_ = conn.Raw(func(driverConn any) error {
		if closer, ok := driverConn.(io.Closer); ok {
			_ = closer.Close()
		}
		// Reporting the connection as bad keeps the pool from reusing it.
		return driver.ErrBadConn
	})
	_ = conn.Close()
}
