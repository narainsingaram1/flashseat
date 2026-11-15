package store

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestBackstopConstraintOnlyMatchesSeatIndexes(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "23505", ConstraintName: "one_live_hold_per_seat"}, true},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505", ConstraintName: "order_items_seat_id_key"}), true},
		{&pgconn.PgError{Code: "23505", ConstraintName: "one_active_hold_per_user"}, false}, // buyer limit, not seat safety
		{&pgconn.PgError{Code: "23505", ConstraintName: "orders_user_id_idempotency_key_key"}, false},
		{&pgconn.PgError{Code: "FS409"}, false}, // the engine's own check refused it: not a backstop hit
		{nil, false},
	}
	for _, c := range cases {
		if _, got := backstopConstraint(c.err); got != c.want {
			t.Errorf("backstopConstraint(%v)=%v, want %v", c.err, got, c.want)
		}
	}
}
