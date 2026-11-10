package store

import "context"

// ExplainRunUsageObservations returns the SQLite query plan of the run-usage
// evidence query so tests can pin its index use.
func (d *DB) ExplainRunUsageObservations(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+runUsageObservationsQuery, "", "~")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return nil, err
		}
		plan = append(plan, detail)
	}
	return plan, rows.Err()
}
