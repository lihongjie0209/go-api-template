package database

import "strings"

var likeLiteralReplacer = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")

// LikeContainsPattern treats user input as literal text for SQL LIKE clauses.
// The corresponding SQL predicate must specify ESCAPE '!' on every LIKE arm.
// This escape character works with PostgreSQL, Kingbase and MySQL without
// depending on the driver's backslash or SQL-mode behavior.
func LikeContainsPattern(value string) string {
	return "%" + likeLiteralReplacer.Replace(value) + "%"
}
