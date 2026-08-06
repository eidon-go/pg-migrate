package db

import "database/sql"

// ExportLockConn exposes the private lockConn field for testing.
func (p *Postgres) ExportLockConn() *sql.Conn {
	return p.lockConn
}
