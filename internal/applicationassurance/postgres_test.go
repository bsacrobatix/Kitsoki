package applicationassurance

import (
	"testing"

	"kitsoki/internal/dbruntime/pgtest"
)

func TestPostgresStoreParity(t *testing.T) {
	db := pgtest.Open(t)
	evidence, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	testStoresRoundTrip(t, evidence)
}
