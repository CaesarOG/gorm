package clause

// Interface clause interface
type Interface interface {
	Name() string
	Build(Builder)
	MergeClause(*Clause)
}

// ClauseBuilder clause builder, allows to custmize how to build clause
type ClauseBuilder interface {
	Build(Clause, Builder)
}

// Builder builder interface
type Builder interface {
	WriteByte(byte) error
	Write(sql ...string) error
	WriteQuoted(field interface{}) error
	AddVar(vars ...interface{}) string
	Quote(field interface{}) string
}

// Clause
type Clause struct {
	Name                 string // WHERE
	Priority             float64
	BeforeExpressions    []Expression
	AfterNameExpressions []Expression
	AfterExpressions     []Expression
	Expression           Expression
	Builder              ClauseBuilder
}

// Build build clause
func (c Clause) Build(builder Builder) {
	if c.Builder != nil {
		c.Builder.Build(c, builder)
	} else {
		builders := c.BeforeExpressions
		if c.Name != "" {
			builders = append(builders, Expr{SQL: c.Name})
		}

		builders = append(builders, c.AfterNameExpressions...)
		if c.Expression != nil {
			builders = append(builders, c.Expression)
		}

		for idx, expr := range append(builders, c.AfterExpressions...) {
			if idx != 0 {
				builder.WriteByte(' ')
			}
			expr.Build(builder)
		}
	}
}

const (
	PrimaryKey   string = "@@@priamry_key@@@"
	CurrentTable string = "@@@table@@@"
)

var (
	currentTable  = Table{Name: CurrentTable}
	PrimaryColumn = Column{Table: CurrentTable, Name: PrimaryKey}
)

// Column quote with name
type Column struct {
	Table string
	Name  string
	Alias string
	Raw   bool
}

// Table quote with name
type Table struct {
	Name  string
	Alias string
	Raw   bool
}
