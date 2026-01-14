package jobdb

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestIsDuplicateKeyError(t *testing.T) {
	if isDuplicateKeyError(nil) {
		t.Fatalf("expected false for nil error")
	}

	otherErr := errors.New("boom")
	if isDuplicateKeyError(otherErr) {
		t.Fatalf("expected false for non-mysql error")
	}

	dupErr := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	if !isDuplicateKeyError(dupErr) {
		t.Fatalf("expected true for duplicate key error")
	}

	otherMySQLErr := &mysql.MySQLError{Number: 1234, Message: "Other"}
	if isDuplicateKeyError(otherMySQLErr) {
		t.Fatalf("expected false for non-duplicate mysql error")
	}
}
