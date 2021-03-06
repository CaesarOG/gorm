package migrator

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"github.com/jinzhu/gorm"
	"github.com/jinzhu/gorm/clause"
	"github.com/jinzhu/gorm/schema"
)

// Migrator m struct
type Migrator struct {
	Config
}

// Config schema config
type Config struct {
	CreateIndexAfterCreateTable             bool
	AllowDeferredConstraintsWhenAutoMigrate bool
	DB                                      *gorm.DB
	gorm.Dialector
}

func (m Migrator) RunWithValue(value interface{}, fc func(*gorm.Statement) error) error {
	stmt := m.DB.Statement
	if stmt == nil {
		stmt = &gorm.Statement{DB: m.DB}
	}

	if err := stmt.Parse(value); err != nil {
		return err
	}

	return fc(stmt)
}

func (m Migrator) DataTypeOf(field *schema.Field) string {
	if field.DBDataType != "" {
		return field.DBDataType
	}

	return m.Dialector.DataTypeOf(field)
}

// AutoMigrate
func (m Migrator) AutoMigrate(values ...interface{}) error {
	// TODO smart migrate data type
	for _, value := range m.ReorderModels(values, true) {
		tx := m.DB.Session(&gorm.Session{})
		if !tx.Migrator().HasTable(value) {
			if err := tx.Migrator().CreateTable(value); err != nil {
				return err
			}
		} else {
			if err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
				for _, field := range stmt.Schema.FieldsByDBName {
					if !tx.Migrator().HasColumn(value, field.DBName) {
						if err := tx.Migrator().AddColumn(value, field.DBName); err != nil {
							return err
						}
					}
				}

				for _, rel := range stmt.Schema.Relationships.Relations {
					if constraint := rel.ParseConstraint(); constraint != nil {
						if !tx.Migrator().HasConstraint(value, constraint.Name) {
							if err := tx.Migrator().CreateConstraint(value, constraint.Name); err != nil {
								return err
							}
						}
					}

					for _, chk := range stmt.Schema.ParseCheckConstraints() {
						if !tx.Migrator().HasConstraint(value, chk.Name) {
							if err := tx.Migrator().CreateConstraint(value, chk.Name); err != nil {
								return err
							}
						}
					}

					// create join table
					if rel.JoinTable != nil {
						joinValue := reflect.New(rel.JoinTable.ModelType).Interface()
						if !tx.Migrator().HasTable(joinValue) {
							defer tx.Migrator().CreateTable(joinValue)
						}
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}

	return nil
}

func (m Migrator) CreateTable(values ...interface{}) error {
	for _, value := range m.ReorderModels(values, false) {
		tx := m.DB.Session(&gorm.Session{})
		if err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
			var (
				createTableSQL          = "CREATE TABLE ? ("
				values                  = []interface{}{clause.Table{Name: stmt.Table}}
				hasPrimaryKeyInDataType bool
			)

			for _, dbName := range stmt.Schema.DBNames {
				field := stmt.Schema.FieldsByDBName[dbName]
				createTableSQL += fmt.Sprintf("? ?")
				hasPrimaryKeyInDataType = hasPrimaryKeyInDataType || strings.Contains(strings.ToUpper(field.DBDataType), "PRIMARY KEY")
				values = append(values, clause.Column{Name: dbName}, clause.Expr{SQL: m.DataTypeOf(field)})

				if field.AutoIncrement {
					createTableSQL += " AUTO_INCREMENT"
				}

				if field.NotNull {
					createTableSQL += " NOT NULL"
				}

				if field.Unique {
					createTableSQL += " UNIQUE"
				}

				if field.DefaultValue != "" {
					createTableSQL += " DEFAULT ?"
					values = append(values, clause.Expr{SQL: field.DefaultValue})
				}
				createTableSQL += ","
			}

			if !hasPrimaryKeyInDataType {
				createTableSQL += "PRIMARY KEY ?,"
				primaryKeys := []interface{}{}
				for _, field := range stmt.Schema.PrimaryFields {
					primaryKeys = append(primaryKeys, clause.Column{Name: field.DBName})
				}

				values = append(values, primaryKeys)
			}

			for _, idx := range stmt.Schema.ParseIndexes() {
				if m.CreateIndexAfterCreateTable {
					tx.Migrator().CreateIndex(value, idx.Name)
				} else {
					createTableSQL += "INDEX ? ?,"
					values = append(values, clause.Expr{SQL: idx.Name}, tx.Migrator().(BuildIndexOptionsInterface).BuildIndexOptions(idx.Fields, stmt))
				}
			}

			for _, rel := range stmt.Schema.Relationships.Relations {
				if constraint := rel.ParseConstraint(); constraint != nil {
					sql, vars := buildConstraint(constraint)
					createTableSQL += sql + ","
					values = append(values, vars...)
				}

				// create join table
				if rel.JoinTable != nil {
					joinValue := reflect.New(rel.JoinTable.ModelType).Interface()
					if !tx.Migrator().HasTable(joinValue) {
						defer tx.Migrator().CreateTable(joinValue)
					}
				}
			}

			for _, chk := range stmt.Schema.ParseCheckConstraints() {
				createTableSQL += "CONSTRAINT ? CHECK ?,"
				values = append(values, clause.Column{Name: chk.Name}, clause.Expr{SQL: chk.Constraint})
			}

			createTableSQL = strings.TrimSuffix(createTableSQL, ",")

			createTableSQL += ")"
			return tx.Exec(createTableSQL, values...).Error
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m Migrator) DropTable(values ...interface{}) error {
	values = m.ReorderModels(values, false)
	for i := len(values) - 1; i >= 0; i-- {
		value := values[i]
		if m.DB.Migrator().HasTable(value) {
			tx := m.DB.Session(&gorm.Session{})
			if err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
				return tx.Exec("DROP TABLE ?", clause.Table{Name: stmt.Table}).Error
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m Migrator) HasTable(value interface{}) bool {
	var count int64
	m.RunWithValue(value, func(stmt *gorm.Statement) error {
		currentDatabase := m.DB.Migrator().CurrentDatabase()
		return m.DB.Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ? AND table_type = ?", currentDatabase, stmt.Table, "BASE TABLE").Row().Scan(&count)
	})

	return count > 0
}

func (m Migrator) RenameTable(oldName, newName string) error {
	return m.DB.Exec("RENAME TABLE ? TO ?", oldName, newName).Error
}

func (m Migrator) AddColumn(value interface{}, field string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if field := stmt.Schema.LookUpField(field); field != nil {
			return m.DB.Exec(
				"ALTER TABLE ? ADD ? ?",
				clause.Table{Name: stmt.Table}, clause.Column{Name: field.DBName}, clause.Expr{SQL: m.DataTypeOf(field)},
			).Error
		}
		return fmt.Errorf("failed to look up field with name: %s", field)
	})
}

func (m Migrator) DropColumn(value interface{}, field string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if field := stmt.Schema.LookUpField(field); field != nil {
			return m.DB.Exec(
				"ALTER TABLE ? DROP COLUMN ?", clause.Table{Name: stmt.Table}, clause.Column{Name: field.DBName},
			).Error
		}
		return fmt.Errorf("failed to look up field with name: %s", field)
	})
}

func (m Migrator) AlterColumn(value interface{}, field string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if field := stmt.Schema.LookUpField(field); field != nil {
			return m.DB.Exec(
				"ALTER TABLE ? ALTER COLUMN ? TYPE ?",
				clause.Table{Name: stmt.Table}, clause.Column{Name: field.DBName}, clause.Expr{SQL: m.DataTypeOf(field)},
			).Error
		}
		return fmt.Errorf("failed to look up field with name: %s", field)
	})
}

func (m Migrator) HasColumn(value interface{}, field string) bool {
	var count int64
	m.RunWithValue(value, func(stmt *gorm.Statement) error {
		currentDatabase := m.DB.Migrator().CurrentDatabase()
		name := field
		if field := stmt.Schema.LookUpField(field); field != nil {
			name = field.DBName
		}

		return m.DB.Raw(
			"SELECT count(*) FROM INFORMATION_SCHEMA.columns WHERE table_schema = ? AND table_name = ? AND column_name = ?",
			currentDatabase, stmt.Table, name,
		).Row().Scan(&count)
	})

	return count > 0
}

func (m Migrator) RenameColumn(value interface{}, oldName, field string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if field := stmt.Schema.LookUpField(field); field != nil {
			oldName = m.DB.NamingStrategy.ColumnName(stmt.Table, oldName)
			return m.DB.Exec(
				"ALTER TABLE ? RENAME COLUMN ? TO ?",
				clause.Table{Name: stmt.Table}, clause.Column{Name: oldName}, clause.Column{Name: field.DBName},
			).Error
		}
		return fmt.Errorf("failed to look up field with name: %s", field)
	})
}

func (m Migrator) ColumnTypes(value interface{}) (columnTypes []*sql.ColumnType, err error) {
	err = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		rows, err := m.DB.Raw("select * from ?", clause.Table{Name: stmt.Table}).Rows()
		if err == nil {
			columnTypes, err = rows.ColumnTypes()
		}
		return err
	})
	return
}

func (m Migrator) CreateView(name string, option gorm.ViewOption) error {
	return gorm.ErrNotImplemented
}

func (m Migrator) DropView(name string) error {
	return gorm.ErrNotImplemented
}

func buildConstraint(constraint *schema.Constraint) (sql string, results []interface{}) {
	sql = "CONSTRAINT ? FOREIGN KEY ? REFERENCES ??"
	if constraint.OnDelete != "" {
		sql += " ON DELETE " + constraint.OnDelete
	}

	if constraint.OnUpdate != "" {
		sql += " ON UPDATE  " + constraint.OnUpdate
	}

	var foreignKeys, references []interface{}
	for _, field := range constraint.ForeignKeys {
		foreignKeys = append(foreignKeys, clause.Column{Name: field.DBName})
	}

	for _, field := range constraint.References {
		references = append(references, clause.Column{Name: field.DBName})
	}
	results = append(results, clause.Table{Name: constraint.Name}, foreignKeys, clause.Table{Name: constraint.ReferenceSchema.Table}, references)
	return
}

func (m Migrator) CreateConstraint(value interface{}, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		checkConstraints := stmt.Schema.ParseCheckConstraints()
		if chk, ok := checkConstraints[name]; ok {
			return m.DB.Exec(
				"ALTER TABLE ? ADD CONSTRAINT ? CHECK ?",
				clause.Table{Name: stmt.Table}, clause.Column{Name: chk.Name}, clause.Expr{SQL: chk.Constraint},
			).Error
		}

		for _, rel := range stmt.Schema.Relationships.Relations {
			if constraint := rel.ParseConstraint(); constraint != nil && constraint.Name == name {
				sql, values := buildConstraint(constraint)
				return m.DB.Exec("ALTER TABLE ? ADD "+sql, append([]interface{}{clause.Table{Name: stmt.Table}}, values...)...).Error
			}
		}

		err := fmt.Errorf("failed to create constraint with name %v", name)
		if field := stmt.Schema.LookUpField(name); field != nil {
			for _, cc := range checkConstraints {
				if err = m.DB.Migrator().CreateIndex(value, cc.Name); err != nil {
					return err
				}
			}

			for _, rel := range stmt.Schema.Relationships.Relations {
				if constraint := rel.ParseConstraint(); constraint != nil && constraint.Field == field {
					if err = m.DB.Migrator().CreateIndex(value, constraint.Name); err != nil {
						return err
					}
				}
			}
		}

		return err
	})
}

func (m Migrator) DropConstraint(value interface{}, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		return m.DB.Exec(
			"ALTER TABLE ? DROP CONSTRAINT ?",
			clause.Table{Name: stmt.Table}, clause.Column{Name: name},
		).Error
	})
}

func (m Migrator) HasConstraint(value interface{}, name string) bool {
	var count int64
	m.RunWithValue(value, func(stmt *gorm.Statement) error {
		currentDatabase := m.DB.Migrator().CurrentDatabase()
		return m.DB.Raw(
			"SELECT count(*) FROM INFORMATION_SCHEMA.referential_constraints WHERE constraint_schema = ? AND table_name = ? AND constraint_name = ?",
			currentDatabase, stmt.Table, name,
		).Row().Scan(&count)
	})

	return count > 0
}

func (m Migrator) BuildIndexOptions(opts []schema.IndexOption, stmt *gorm.Statement) (results []interface{}) {
	for _, opt := range opts {
		str := stmt.Quote(opt.DBName)
		if opt.Expression != "" {
			str = opt.Expression
		} else if opt.Length > 0 {
			str += fmt.Sprintf("(%d)", opt.Length)
		}

		if opt.Collate != "" {
			str += " COLLATE " + opt.Collate
		}

		if opt.Sort != "" {
			str += " " + opt.Sort
		}
		results = append(results, clause.Expr{SQL: str})
	}
	return
}

type BuildIndexOptionsInterface interface {
	BuildIndexOptions([]schema.IndexOption, *gorm.Statement) []interface{}
}

func (m Migrator) CreateIndex(value interface{}, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		err := fmt.Errorf("failed to create index with name %v", name)
		indexes := stmt.Schema.ParseIndexes()

		if idx, ok := indexes[name]; ok {
			opts := m.DB.Migrator().(BuildIndexOptionsInterface).BuildIndexOptions(idx.Fields, stmt)
			values := []interface{}{clause.Column{Name: idx.Name}, clause.Table{Name: stmt.Table}, opts}

			createIndexSQL := "CREATE "
			if idx.Class != "" {
				createIndexSQL += idx.Class + " "
			}
			createIndexSQL += "INDEX ? ON ??"

			if idx.Comment != "" {
				values = append(values, idx.Comment)
				createIndexSQL += " COMMENT ?"
			}

			if idx.Type != "" {
				createIndexSQL += " USING " + idx.Type
			}

			return m.DB.Exec(createIndexSQL, values...).Error
		} else if field := stmt.Schema.LookUpField(name); field != nil {
			for _, idx := range indexes {
				for _, idxOpt := range idx.Fields {
					if idxOpt.Field == field {
						if err = m.CreateIndex(value, idx.Name); err != nil {
							return err
						}
					}
				}
			}
		}
		return err
	})
}

func (m Migrator) DropIndex(value interface{}, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		return m.DB.Exec("DROP INDEX ? ON ?", clause.Column{Name: name}, clause.Table{Name: stmt.Table}).Error
	})
}

func (m Migrator) HasIndex(value interface{}, name string) bool {
	var count int64
	m.RunWithValue(value, func(stmt *gorm.Statement) error {
		currentDatabase := m.DB.Migrator().CurrentDatabase()
		return m.DB.Raw(
			"SELECT count(*) FROM information_schema.statistics WHERE table_schema = ? AND table_name = ? AND index_name = ?",
			currentDatabase, stmt.Table, name,
		).Row().Scan(&count)
	})

	return count > 0
}

func (m Migrator) RenameIndex(value interface{}, oldName, newName string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		return m.DB.Exec(
			"ALTER TABLE ? RENAME INDEX ? TO ?",
			clause.Table{Name: stmt.Table}, clause.Column{Name: oldName}, clause.Column{Name: newName},
		).Error
	})
}

func (m Migrator) CurrentDatabase() (name string) {
	m.DB.Raw("SELECT DATABASE()").Row().Scan(&name)
	return
}

// ReorderModels reorder models according to constraint dependencies
func (m Migrator) ReorderModels(values []interface{}, autoAdd bool) (results []interface{}) {
	type Dependency struct {
		*gorm.Statement
		Depends []*schema.Schema
	}

	var (
		modelNames, orderedModelNames []string
		orderedModelNamesMap          = map[string]bool{}
		valuesMap                     = map[string]Dependency{}
		insertIntoOrderedList         func(name string)
	)

	parseDependence := func(value interface{}, addToList bool) {
		dep := Dependency{
			Statement: &gorm.Statement{DB: m.DB, Dest: value},
		}
		dep.Parse(value)

		for _, rel := range dep.Schema.Relationships.Relations {
			if c := rel.ParseConstraint(); c != nil && c.Schema != c.ReferenceSchema {
				dep.Depends = append(dep.Depends, c.ReferenceSchema)
			}
		}

		valuesMap[dep.Schema.Table] = dep

		if addToList {
			modelNames = append(modelNames, dep.Schema.Table)
		}
	}

	insertIntoOrderedList = func(name string) {
		if _, ok := orderedModelNamesMap[name]; ok {
			return // avoid loop
		}

		dep := valuesMap[name]
		for _, d := range dep.Depends {
			if _, ok := valuesMap[d.Table]; ok {
				insertIntoOrderedList(d.Table)
			} else if autoAdd {
				parseDependence(reflect.New(d.ModelType).Interface(), autoAdd)
				insertIntoOrderedList(d.Table)
			}
		}

		orderedModelNames = append(orderedModelNames, name)
		orderedModelNamesMap[name] = true
	}

	for _, value := range values {
		parseDependence(value, true)
	}

	for _, name := range modelNames {
		insertIntoOrderedList(name)
	}

	for _, name := range orderedModelNames {
		results = append(results, valuesMap[name].Statement.Dest)
	}
	return
}
