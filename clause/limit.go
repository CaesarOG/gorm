package clause

import "strconv"

// Limit limit clause
type Limit struct {
	Limit  int
	Offset int
}

// Name where clause name
func (limit Limit) Name() string {
	return "LIMIT"
}

// Build build where clause
func (limit Limit) Build(builder Builder) {
	if limit.Limit > 0 {
		builder.Write("LIMIT ")
		builder.Write(strconv.Itoa(limit.Limit))

		if limit.Offset > 0 {
			builder.Write(" OFFSET ")
			builder.Write(strconv.Itoa(limit.Offset))
		}
	}
}

// MergeClause merge order by clauses
func (limit Limit) MergeClause(clause *Clause) {
	clause.Name = ""

	if v, ok := clause.Expression.(Limit); ok {
		if limit.Limit == 0 && v.Limit > 0 {
			limit.Limit = v.Limit
		}

		if limit.Offset == 0 && v.Offset > 0 {
			limit.Offset = v.Offset
		}
	}

	clause.Expression = limit
}
