package bun

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/uptrace/bun/dialect/feature"
	"github.com/uptrace/bun/internal"
	"github.com/uptrace/bun/schema"
)

type relationJoin struct {
	Parent    *relationJoin
	BaseModel TableModel
	JoinModel TableModel
	Relation  *schema.Relation

	additionalJoinOnConditions []schema.QueryWithArgs

	apply   func(*SelectQuery) *SelectQuery
	columns []schema.QueryWithArgs
}

// relationJoinBuilder encapsulates SQL building logic for manyQueryWithWindowFunction.
// It separates SQL construction concerns from the relationJoin struct.
type relationJoinBuilder struct{}

func (j *relationJoin) applyTo(q *SelectQuery) {
	if j.apply == nil {
		return
	}

	var table *schema.Table
	var columns []schema.QueryWithArgs

	// Save state.
	table, q.table = q.table, j.JoinModel.Table()
	columns, q.columns = q.columns, nil

	q = j.apply(q)

	// Restore state.
	q.table = table
	j.columns, q.columns = q.columns, columns
}

func (j *relationJoin) selectMany(ctx context.Context, q *SelectQuery) error {
	q = j.manyQuery(q)
	if q == nil {
		return nil
	}
	return q.Scan(ctx)
}

func (j *relationJoin) manyQuery(q *SelectQuery) *SelectQuery {
	hasManyModel := newHasManyModel(j)
	if hasManyModel == nil {
		return nil
	}

	q = q.Model(hasManyModel)

	// 先执行 apply 函数，让用户可以设置 limit/offset 等条件
	j.applyTo(q)

	// 检测是否设置了 limit/offset，且 dialect 支持窗口函数
	if q.limit > 0 || q.offset > 0 {
		if q.db.HasFeature(feature.WindowFunctions) {
			return j.manyQueryWithWindowFunction(q.limit, q.offset, q)
		} else {
			internal.Warn.Printf("relation select many, but the dialect without window functions, limit will add to out query")
		}
	}

	var where []byte

	if q.db.HasFeature(feature.CompositeIn) {
		return j.manyQueryCompositeIn(where, q)
	}
	return j.manyQueryMulti(where, q)
}

func (j *relationJoin) manyQueryCompositeIn(where []byte, q *SelectQuery) *SelectQuery {
	if len(j.Relation.JoinPKs) > 1 {
		where = append(where, '(')
	}
	where = appendColumns(where, j.JoinModel.Table().SQLAlias, j.Relation.JoinPKs)
	if len(j.Relation.JoinPKs) > 1 {
		where = append(where, ')')
	}
	where = append(where, " IN ("...)
	where = appendChildValues(
		q.db.QueryGen(),
		where,
		j.JoinModel.rootValue(),
		j.JoinModel.parentIndex(),
		j.Relation.BasePKs,
	)
	where = append(where, ")"...)
	if len(j.additionalJoinOnConditions) > 0 {
		where = append(where, " AND "...)
		where = appendAdditionalJoinOnConditions(q.db.QueryGen(), where, j.additionalJoinOnConditions)
	}

	q = q.Where(internal.String(where))

	if j.Relation.PolymorphicField != nil {
		q = q.Where("? = ?", j.Relation.PolymorphicField.SQLName, j.Relation.PolymorphicValue)
	}

	j.applyTo(q)
	q = q.Apply(j.hasManyColumns)

	return q
}

func (j *relationJoin) manyQueryMulti(where []byte, q *SelectQuery) *SelectQuery {
	where = appendMultiValues(
		q.db.QueryGen(),
		where,
		j.JoinModel.rootValue(),
		j.JoinModel.parentIndex(),
		j.Relation.BasePKs,
		j.Relation.JoinPKs,
		j.JoinModel.Table().SQLAlias,
	)

	q = q.Where(internal.String(where))

	if len(j.additionalJoinOnConditions) > 0 {
		q = q.Where(internal.String(appendAdditionalJoinOnConditions(q.db.QueryGen(), nil, j.additionalJoinOnConditions)))
	}

	if j.Relation.PolymorphicField != nil {
		q = q.Where("? = ?", j.Relation.PolymorphicField.SQLName, j.Relation.PolymorphicValue)
	}

	j.applyTo(q)
	q = q.Apply(j.hasManyColumns)

	return q
}

// manyQueryWithWindowFunction uses subquery with window function to implement
// has-many relation with limit/offset. This ensures each parent record gets
// the correct number of child records.
//
// The generated SQL structure is:
// SELECT columns FROM table WHERE conditions AND id IN (
//
//	SELECT _t.id FROM (
//	    SELECT ALIAS.id, ROW_NUMBER() OVER (PARTITION BY ... ORDER BY ...) AS _row_num
//	    FROM table
//	    WHERE conditions
//	) AS _t
//	WHERE _t._row_num <= ?
//
// )
func (j *relationJoin) manyQueryWithWindowFunction(limit, offset int64, q *SelectQuery) *SelectQuery {
	gen := q.db.gen
	joinTable := j.JoinModel.Table()
	builder := relationJoinBuilder{}

	// Build the WHERE clause for the inner query
	where := builder.buildBaseWhereClause(j, gen, joinTable)

	// Apply WHERE conditions to a temporary query to extract user conditions
	// (except limit/offset which we handle via window function).
	tempQ := &SelectQuery{
		whereBaseQuery: whereBaseQuery{
			baseQuery: baseQuery{
				db: q.db,
			},
		},
	}
	tempQ = tempQ.Model(j.JoinModel)
	tempQ = tempQ.Where(where)
	j.applyTo(tempQ)

	// Reset limit/offset in the original query since we'll handle them via window function
	q.setLimit(0)
	q.setOffset(0)

	// Copy all user conditions from tempQ to the inner query.
	// CopyConditionsFrom handles all condition types (WHERE, GROUP BY, HAVING, etc.)
	// and automatically skips limit/offset, so new condition fields added to SelectQuery
	// will be included without requiring manual updates here.
	q.CopyConditionsFrom(tempQ)

	// Clear ORDER BY from the inner query because:
	// 1. We've already extracted orderByCols from tempQ.order for the window function
	// 2. The ORDER BY in the subquery is unnecessary since we only use id and _row_num
	// 3. The window function already defines the ordering
	q.order = nil

	// Build window function
	// PARTITION BY: using the join keys (foreign key in child table)
	partitionCols := builder.buildPartitionColumns(joinTable, j.Relation.JoinPKs)

	// ORDER BY: from user conditions or default to partition columns
	orderByCols := builder.buildOrderByColumns(gen, tempQ, partitionCols)

	// Build window expression using strings.Builder (no fmt.Sprintf)
	windowExpr := builder.buildWindowExpression(partitionCols, orderByCols)

	// Get primary key fields for WHERE IN clause
	pkFields := joinTable.PKs
	if len(pkFields) == 0 {
		pkFields = j.Relation.BasePKs
	}

	// Add primary key columns and window function to SELECT using strings.Builder
	builder.addSelectColumns(q, joinTable, pkFields, windowExpr)

	// Generate the inner query SQL
	innerSQL, err := q.AppendQuery(gen, nil)
	if err != nil {
		q.setErr(err)
		return q
	}

	// Build the outer query
	outerQ := &SelectQuery{
		whereBaseQuery: whereBaseQuery{
			baseQuery: baseQuery{
				db: q.db,
			},
		},
	}
	outerQ = outerQ.Model(q.tableModel)

	// Note: We DON'T apply base WHERE conditions or user conditions to outer query here
	// because:
	// 1. The subquery already filters by menu_id, code, deleted_at, etc.
	// 2. The outer query only needs to filter by id IN (subquery results)
	// 3. Applying redundant conditions would make the SQL unnecessarily verbose
	// We'll only apply conditions that are NOT already in the subquery, if any.

	// Build WHERE IN clause: id IN (SELECT _t.id FROM (innerSQL) AS _t WHERE _row_num <= ?)
	// Using strings.Builder with Grow pre-allocation
	whereInClause := builder.buildWhereInClause(joinTable, pkFields, limit, offset)

	// Apply WHERE IN clause with appropriate arguments
	if offset > 0 {
		if limit > 0 {
			// Both offset and limit: _row_num > offset AND _row_num <= offset+limit
			outerQ = outerQ.Where(whereInClause,
				schema.SafeQuery(string(innerSQL), nil),
				offset, offset+limit)
		} else {
			// Only offset: _row_num > offset
			outerQ = outerQ.Where(whereInClause,
				schema.SafeQuery(string(innerSQL), nil),
				offset)
		}
	} else if limit > 0 {
		// Only limit: _row_num <= limit
		outerQ = outerQ.Where(whereInClause,
			schema.SafeQuery(string(innerSQL), nil),
			limit)
	}

	// Select the actual columns needed
	outerQ = outerQ.Apply(j.hasManyColumns)

	return outerQ
}

func (j *relationJoin) hasManyColumns(q *SelectQuery) *SelectQuery {
	b := make([]byte, 0, 32)

	joinTable := j.JoinModel.Table()
	if len(j.columns) > 0 {
		for i, col := range j.columns {
			if i > 0 {
				b = append(b, ", "...)
			}

			if col.Args == nil {
				if field, ok := joinTable.FieldMap[col.Query]; ok {
					b = append(b, joinTable.SQLAlias...)
					b = append(b, '.')
					b = append(b, field.SQLName...)
					continue
				}
			}

			var err error
			b, err = col.AppendQuery(q.db.gen, b)
			if err != nil {
				q.setErr(err)
				return q
			}

		}
	} else {
		b = appendColumns(b, joinTable.SQLAlias, joinTable.Fields)
	}

	q = q.ColumnExpr(internal.String(b))

	return q
}

func (j *relationJoin) selectM2M(ctx context.Context, q *SelectQuery) error {
	q = j.m2mQuery(q)
	if q == nil {
		return nil
	}
	return q.Scan(ctx)
}

func (j *relationJoin) m2mQuery(q *SelectQuery) *SelectQuery {
	gen := q.db.gen

	m2mModel := newM2MModel(j)
	if m2mModel == nil {
		return nil
	}
	q = q.Model(m2mModel)

	// 先执行 apply 函数，让用户可以设置 limit/offset 等条件
	j.applyTo(q)

	// 检测是否设置了 limit/offset，且 dialect 支持窗口函数
	if q.limit > 0 || q.offset > 0 {
		if q.db.HasFeature(feature.WindowFunctions) {
			return j.m2mQueryWithWindowFunction(q.limit, q.offset, q)
		} else {
			internal.Warn.Printf("relation select m2m, but the dialect without window functions, limit will add to out query")
		}
	}

	index := j.JoinModel.parentIndex()

	if j.Relation.M2MTable != nil {
		// We only need base pks to park joined models to the base model.
		fields := j.Relation.M2MBasePKs

		b := make([]byte, 0, len(fields))
		b = appendColumns(b, j.Relation.M2MTable.SQLAlias, fields)

		q = q.ColumnExpr(internal.String(b))
	}

	//nolint
	var join []byte
	join = append(join, "JOIN "...)
	join = gen.AppendQuery(join, string(j.Relation.M2MTable.SQLName))
	join = append(join, " AS "...)
	join = append(join, j.Relation.M2MTable.SQLAlias...)
	join = append(join, " ON ("...)
	for i, col := range j.Relation.M2MBasePKs {
		if i > 0 {
			join = append(join, ", "...)
		}
		join = append(join, j.Relation.M2MTable.SQLAlias...)
		join = append(join, '.')
		join = append(join, col.SQLName...)
	}
	join = append(join, ") IN ("...)
	join = appendChildValues(gen, join, j.BaseModel.rootValue(), index, j.Relation.BasePKs)
	join = append(join, ")"...)

	if len(j.additionalJoinOnConditions) > 0 {
		join = append(join, " AND "...)
		join = appendAdditionalJoinOnConditions(gen, join, j.additionalJoinOnConditions)
	}

	q = q.Join(internal.String(join))

	joinTable := j.JoinModel.Table()
	for i, m2mJoinField := range j.Relation.M2MJoinPKs {
		joinField := j.Relation.JoinPKs[i]
		q = q.Where("?.? = ?.?",
			joinTable.SQLAlias, joinField.SQLName,
			j.Relation.M2MTable.SQLAlias, m2mJoinField.SQLName)
	}

	q = q.Apply(j.hasManyColumns)

	return q
}

// m2mQueryWithWindowFunction uses subquery with window function to implement
// many-to-many relation with limit/offset. This ensures each parent record gets
// the correct number of child records through the join table.
//
// The generated SQL structure is:
//
//	outer: SELECT bg.book_id, genre.* FROM genres AS genre
//	       JOIN book_genres AS bg ON bg.genre_id = genre.id
//	       WHERE (bg.book_id, ...) IN (base_values)
//	       AND genre.id IN (
//	         inner: SELECT _t.id FROM (
//	           SELECT genre.id, ROW_NUMBER() OVER (PARTITION BY bg.book_id ORDER BY genre.id) AS _row_num
//	           FROM genres AS genre
//	           JOIN book_genres AS bg ON bg.genre_id = genre.id
//	           WHERE (bg.book_id, ...) IN (base_values)
//	         ) AS _t WHERE _t._row_num <= ?
//	       )
func (j *relationJoin) m2mQueryWithWindowFunction(limit, offset int64, q *SelectQuery) *SelectQuery {
	gen := q.db.gen
	joinTable := j.JoinModel.Table()
	m2mTable := j.Relation.M2MTable
	builder := relationJoinBuilder{}

	// Build the inner query from scratch with proper JOINs.
	// Step 1: Extract user conditions via a temporary query.
	tempQ := &SelectQuery{
		whereBaseQuery: whereBaseQuery{
			baseQuery: baseQuery{
				db: q.db,
			},
		},
	}
	tempQ = tempQ.Model(j.JoinModel)
	j.applyTo(tempQ)

	// Step 2: Build the inner query.
	innerQ := &SelectQuery{
		whereBaseQuery: whereBaseQuery{
			baseQuery: baseQuery{
				db: q.db,
			},
		},
	}
	innerQ = innerQ.Model(j.JoinModel)

	// Copy user conditions (WHERE, GROUP BY, HAVING) from tempQ BEFORE adding JOINs,
	// because CopyConditionsFrom overwrites joins.
	innerQ.CopyConditionsFrom(tempQ)

	// Add JOIN to m2m table in the inner query (must be after CopyConditionsFrom).
	innerQ = j.appendM2MJoin(gen, innerQ)

	// Build window function.
	// PARTITION BY: using the m2m base pk (foreign key in m2m table that references base table).
	partitionCols := builder.buildPartitionColumns(m2mTable, j.Relation.M2MBasePKs)
	// ORDER BY: from user conditions or default to partition columns.
	orderByCols := builder.buildOrderByColumns(gen, tempQ, partitionCols)
	windowExpr := builder.buildWindowExpression(partitionCols, orderByCols)

	// Get primary key fields for the join table.
	pkFields := joinTable.PKs
	if len(pkFields) == 0 {
		pkFields = j.Relation.JoinPKs
	}

	// Add primary key columns and window function to the inner SELECT.
	builder.addSelectColumns(innerQ, joinTable, pkFields, windowExpr)

	// Clear ORDER BY from the inner query since the window function defines the ordering.
	innerQ.order = nil

	// Generate the inner query SQL.
	innerSQL, err := innerQ.AppendQuery(gen, nil)
	if err != nil {
		q.setErr(err)
		return q
	}

	// Step 3: Build the outer query.
	outerQ := &SelectQuery{
		whereBaseQuery: whereBaseQuery{
			baseQuery: baseQuery{
				db: q.db,
			},
		},
	}
	outerQ = outerQ.Model(q.tableModel)

	// Build WHERE IN clause using join table's primary keys.
	whereInClause := builder.buildWhereInClause(joinTable, pkFields, limit, offset)

	// Apply WHERE IN clause with appropriate arguments.
	if offset > 0 {
		if limit > 0 {
			outerQ = outerQ.Where(whereInClause,
				schema.SafeQuery(string(innerSQL), nil),
				offset, offset+limit)
		} else {
			outerQ = outerQ.Where(whereInClause,
				schema.SafeQuery(string(innerSQL), nil),
				offset)
		}
	} else if limit > 0 {
		outerQ = outerQ.Where(whereInClause,
			schema.SafeQuery(string(innerSQL), nil),
			limit)
	}

	// Add the m2m base pk columns and JOINs to the outer query.
	if m2mTable != nil {
		fields := j.Relation.M2MBasePKs
		b := make([]byte, 0, len(fields))
		b = appendColumns(b, m2mTable.SQLAlias, fields)
		outerQ = outerQ.ColumnExpr(internal.String(b))
	}

	outerQ = j.appendM2MJoin(gen, outerQ)

	// Select the actual columns needed.
	outerQ = outerQ.Apply(j.hasManyColumns)

	return outerQ
}

// appendM2MJoin adds the m2m JOIN and base table filter to a SelectQuery.
// This produces:
//
//	JOIN m2m_table AS alias ON (m2m_base_pk, ...) IN (base_values)
//	WHERE m2m_table.m2m_join_pk = join_table.join_pk
func (j *relationJoin) appendM2MJoin(gen schema.QueryGen, q *SelectQuery) *SelectQuery {
	m2mTable := j.Relation.M2MTable

	// Build JOIN: m2m_table AS alias ON (m2m_base_pk, ...) IN (base_values)
	var join []byte
	join = append(join, "JOIN "...)
	join = gen.AppendQuery(join, string(m2mTable.SQLName))
	join = append(join, " AS "...)
	join = append(join, m2mTable.SQLAlias...)
	join = append(join, " ON ("...)
	for i, col := range j.Relation.M2MBasePKs {
		if i > 0 {
			join = append(join, ", "...)
		}
		join = append(join, m2mTable.SQLAlias...)
		join = append(join, '.')
		join = append(join, col.SQLName...)
	}
	join = append(join, ") IN ("...)
	join = appendChildValues(gen, join, j.BaseModel.rootValue(), j.JoinModel.parentIndex(), j.Relation.BasePKs)
	join = append(join, ")"...)

	if len(j.additionalJoinOnConditions) > 0 {
		join = append(join, " AND "...)
		join = appendAdditionalJoinOnConditions(gen, join, j.additionalJoinOnConditions)
	}

	q = q.Join(internal.String(join))

	// Build WHERE: join_table.join_pk = m2m_table.m2m_join_pk
	joinTable := j.JoinModel.Table()
	for i, m2mJoinField := range j.Relation.M2MJoinPKs {
		joinField := j.Relation.JoinPKs[i]
		q = q.Where("?.? = ?.?",
			joinTable.SQLAlias, joinField.SQLName,
			m2mTable.SQLAlias, m2mJoinField.SQLName)
	}

	return q
}

func (j *relationJoin) hasParent() bool {
	if j.Parent != nil {
		switch j.Parent.Relation.Type {
		case schema.HasOneRelation, schema.BelongsToRelation:
			return true
		}
	}
	return false
}

func (j *relationJoin) appendAlias(gen schema.QueryGen, b []byte) []byte {
	quote := gen.IdentQuote()

	b = append(b, quote)
	b = appendAlias(b, j)
	b = append(b, quote)
	return b
}

func (j *relationJoin) appendAliasColumn(gen schema.QueryGen, b []byte, column string) []byte {
	quote := gen.IdentQuote()

	b = append(b, quote)
	b = appendAlias(b, j)
	b = append(b, "__"...)
	b = append(b, column...)
	b = append(b, quote)
	return b
}

func (j *relationJoin) appendBaseAlias(gen schema.QueryGen, b []byte) []byte {
	quote := gen.IdentQuote()

	if j.hasParent() {
		b = append(b, quote)
		b = appendAlias(b, j.Parent)
		b = append(b, quote)
		return b
	}
	return append(b, j.BaseModel.Table().SQLAlias...)
}

func (j *relationJoin) appendSoftDelete(
	gen schema.QueryGen, b []byte, flags internal.Flag,
) []byte {
	b = append(b, '.')

	field := j.JoinModel.Table().SoftDeleteField
	b = append(b, field.SQLName...)

	if field.IsPtr || field.NullZero {
		if flags.Has(deletedFlag) {
			b = append(b, " IS NOT NULL"...)
		} else {
			b = append(b, " IS NULL"...)
		}
	} else {
		if flags.Has(deletedFlag) {
			b = append(b, " != "...)
		} else {
			b = append(b, " = "...)
		}
		b = gen.Dialect().AppendTime(b, time.Time{})
	}

	return b
}

func appendAlias(b []byte, j *relationJoin) []byte {
	if j.hasParent() {
		b = appendAlias(b, j.Parent)
		b = append(b, "__"...)
	}
	b = append(b, j.Relation.Field.Name...)
	return b
}

func (j *relationJoin) appendHasOneJoin(
	gen schema.QueryGen, b []byte, q *SelectQuery,
) (_ []byte, err error) {
	isSoftDelete := j.JoinModel.Table().SoftDeleteField != nil && !q.flags.Has(allWithDeletedFlag)

	b = append(b, "LEFT JOIN "...)
	b = gen.AppendQuery(b, string(j.JoinModel.Table().SQLNameForSelects))
	b = append(b, " AS "...)
	b = j.appendAlias(gen, b)

	b = append(b, " ON "...)

	b = append(b, '(')
	for i, baseField := range j.Relation.BasePKs {
		if i > 0 {
			b = append(b, " AND "...)
		}
		b = j.appendAlias(gen, b)
		b = append(b, '.')
		b = append(b, j.Relation.JoinPKs[i].SQLName...)
		b = append(b, " = "...)
		b = j.appendBaseAlias(gen, b)
		b = append(b, '.')
		b = append(b, baseField.SQLName...)
	}
	b = append(b, ')')

	if isSoftDelete {
		b = append(b, " AND "...)
		b = j.appendAlias(gen, b)
		b = j.appendSoftDelete(gen, b, q.flags)
	}

	if len(j.additionalJoinOnConditions) > 0 {
		b = append(b, " AND "...)
		b = appendAdditionalJoinOnConditions(gen, b, j.additionalJoinOnConditions)
	}

	return b, nil
}

func appendChildValues(
	gen schema.QueryGen, b []byte, v reflect.Value, index []int, fields []*schema.Field,
) []byte {
	seen := make(map[string]struct{})
	walk(v, index, func(v reflect.Value) {
		start := len(b)

		if len(fields) > 1 {
			b = append(b, '(')
		}
		for i, f := range fields {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = f.AppendValue(gen, b, v)
		}
		if len(fields) > 1 {
			b = append(b, ')')
		}
		b = append(b, ", "...)

		if _, ok := seen[string(b[start:])]; ok {
			b = b[:start]
		} else {
			seen[string(b[start:])] = struct{}{}
		}
	})
	if len(seen) > 0 {
		b = b[:len(b)-2] // trim ", "
	}
	return b
}

// appendMultiValues is an alternative to appendChildValues that doesn't use the sql keyword ID
// but instead uses old style ((k1=v1) AND (k2=v2)) OR (...) conditions.
func appendMultiValues(
	gen schema.QueryGen, b []byte, v reflect.Value, index []int, baseFields, joinFields []*schema.Field, joinTable schema.Safe,
) []byte {
	// This is based on a mix of appendChildValues and query_base.appendColumns

	// These should never mismatch in length but nice to know if it does
	if len(joinFields) != len(baseFields) {
		panic("not reached")
	}

	// walk the relations
	b = append(b, '(')
	seen := make(map[string]struct{})
	walk(v, index, func(v reflect.Value) {
		start := len(b)
		for i, f := range baseFields {
			if i > 0 {
				b = append(b, " AND "...)
			}
			if len(baseFields) > 1 {
				b = append(b, '(')
			}
			// Field name
			b = append(b, joinTable...)
			b = append(b, '.')
			b = append(b, []byte(joinFields[i].SQLName)...)

			// Equals value
			b = append(b, '=')
			b = f.AppendValue(gen, b, v)
			if len(baseFields) > 1 {
				b = append(b, ')')
			}
		}

		b = append(b, ") OR ("...)

		if _, ok := seen[string(b[start:])]; ok {
			b = b[:start]
		} else {
			seen[string(b[start:])] = struct{}{}
		}
	})
	if len(seen) > 0 {
		b = b[:len(b)-6] // trim ") OR ("
	}
	b = append(b, ')')
	return b
}

// buildBaseWhereClause constructs the base WHERE clause for the inner query.
func (b *relationJoinBuilder) buildBaseWhereClause(j *relationJoin, gen schema.QueryGen, joinTable *schema.Table) string {
	// Pre-allocate for WHERE clause: columns + " IN (...)" + conditions
	estimatedSize := 128 + len(j.Relation.JoinPKs)*30
	var sb strings.Builder
	sb.Grow(estimatedSize)

	// Build (col1, col2) IN (...)
	if len(j.Relation.JoinPKs) > 1 {
		sb.WriteByte('(')
	}
	for i, pk := range j.Relation.JoinPKs {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(string(joinTable.SQLAlias))
		sb.WriteByte('.')
		sb.WriteString(string(pk.SQLName))
	}
	if len(j.Relation.JoinPKs) > 1 {
		sb.WriteByte(')')
	}
	sb.WriteString(" IN (")

	// We need to append the actual values using the existing appendChildValues
	// Convert to string builder pattern
	var whereBuf []byte
	whereBuf = append(whereBuf, []byte(sb.String())...)
	whereBuf = appendChildValues(
		gen,
		whereBuf,
		j.JoinModel.rootValue(),
		j.JoinModel.parentIndex(),
		j.Relation.BasePKs,
	)
	whereBuf = append(whereBuf, ")"...)

	// Reset builder and append the WHERE clause with values
	sb.Reset()
	sb.Grow(len(whereBuf) + 128)
	sb.Write(whereBuf)

	// Apply additional join on conditions (user filter conditions)
	if len(j.additionalJoinOnConditions) > 0 {
		sb.WriteString(" AND ")
		condBuf := appendAdditionalJoinOnConditions(gen, nil, j.additionalJoinOnConditions)
		sb.Write(condBuf)
	}

	// Apply polymorphic field
	if j.Relation.PolymorphicField != nil {
		sb.WriteString(" AND ")
		sb.WriteString(string(j.Relation.PolymorphicField.SQLName))
		sb.WriteString(" = ")
		valBuf := gen.AppendQuery(nil, j.Relation.PolymorphicValue)
		sb.Write(valBuf)
	}

	return sb.String()
}

// buildPartitionColumns builds the PARTITION BY clause using strings.Builder for efficiency.
func (b *relationJoinBuilder) buildPartitionColumns(joinTable *schema.Table, joinPKs []*schema.Field) string {
	// Pre-allocate: average column name ~20 chars, plus ", " separator
	estimatedSize := len(joinPKs) * (len(joinTable.SQLAlias) + 25)
	var sb strings.Builder
	sb.Grow(estimatedSize)

	for i, pk := range joinPKs {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(string(joinTable.SQLAlias))
		sb.WriteByte('.')
		sb.WriteString(string(pk.SQLName))
	}

	return sb.String()
}

// buildOrderByColumns builds the ORDER BY clause using strings.Builder for efficiency.
func (b *relationJoinBuilder) buildOrderByColumns(gen schema.QueryGen, tempQ *SelectQuery, defaultPartitionCols string) string {
	if len(tempQ.order) > 0 {
		// Build from user conditions
		// Pre-allocate: "ORDER BY " (9 bytes) + average condition (40 bytes) * number of conditions
		estimatedSize := 9 + len(tempQ.order)*40
		var sb strings.Builder
		sb.Grow(estimatedSize)

		for i, o := range tempQ.order {
			if i > 0 {
				sb.WriteString(", ")
			}
			// We can't use the builder directly with AppendQuery since it expects []byte
			// Use a temporary buffer for each order expression
			tmpBuf := make([]byte, 0, 128)
			tmpBuf, _ = o.AppendQuery(gen, tmpBuf)
			sb.Write(tmpBuf)
		}

		return sb.String()
	}
	// Default to partition columns
	return defaultPartitionCols
}

// buildWindowExpression constructs the window function expression without fmt.Sprintf.
func (b *relationJoinBuilder) buildWindowExpression(partitionCols, orderByCols string) string {
	// Pre-allocate: "ROW_NUMBER() OVER (PARTITION BY " + partitionCols + " ORDER BY " + orderByCols + ")"
	estimatedSize := 31 + len(partitionCols) + 11 + len(orderByCols) + 1
	var sb strings.Builder
	sb.Grow(estimatedSize)

	sb.WriteString("ROW_NUMBER() OVER (PARTITION BY ")
	sb.WriteString(partitionCols)
	sb.WriteString(" ORDER BY ")
	sb.WriteString(orderByCols)
	sb.WriteByte(')')

	return sb.String()
}

// addSelectColumns adds the primary key columns and window function to SELECT using strings.Builder.
func (b *relationJoinBuilder) addSelectColumns(q *SelectQuery, joinTable *schema.Table, pkFields []*schema.Field, windowExpr string) {
	// Pre-allocate for column names: alias + '.' + column name
	estimatedSize := len(joinTable.SQLAlias) + 1 + 20

	for _, pk := range pkFields {
		var sb strings.Builder
		sb.Grow(estimatedSize)

		sb.WriteString(string(joinTable.SQLAlias))
		sb.WriteByte('.')
		sb.WriteString(string(pk.SQLName))

		q.addColumn(schema.QueryWithArgs{Query: sb.String(), Args: []any{}})
	}

	// Add window function column
	q.addColumn(schema.QueryWithArgs{Query: windowExpr + " AS _row_num", Args: []any{}})
}

// buildWhereInClause constructs the WHERE IN clause using strings.Builder for efficiency.
func (b *relationJoinBuilder) buildWhereInClause(joinTable *schema.Table, pkFields []*schema.Field, limit, offset int64) string {
	// Pre-allocate: roughly estimate based on number of PKs and template length
	// Template: "(pk1, pk2) IN (SELECT _t.pk1, _t.pk2 FROM (?) AS _t WHERE _t._row_num <= ?)"
	estimatedSize := 100 + len(pkFields)*30
	var sb strings.Builder
	sb.Grow(estimatedSize)

	// Build id IN (...)
	if len(pkFields) == 1 {
		sb.WriteString(string(joinTable.SQLAlias))
		sb.WriteByte('.')
		sb.WriteString(string(pkFields[0].SQLName))
		sb.WriteString(" IN (")
	} else {
		sb.WriteByte('(')
		for i, pk := range pkFields {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(string(joinTable.SQLAlias))
			sb.WriteByte('.')
			sb.WriteString(string(pk.SQLName))
		}
		sb.WriteString(") IN (")
	}

	// Build SELECT _t.id FROM (...) AS _t WHERE _t._row_num <= ?
	sb.WriteString("SELECT _t.")
	for i, pk := range pkFields {
		if i > 0 {
			sb.WriteString(", _t.")
		}
		sb.WriteString(string(pk.SQLName))
	}
	sb.WriteString(" FROM (?) AS _t WHERE ")

	// Build row number filter condition
	if offset > 0 {
		if limit > 0 {
			// _row_num > offset AND _row_num <= offset+limit
			sb.WriteString("_t._row_num > ? AND _t._row_num <= ?")
		} else {
			// _row_num > offset
			sb.WriteString("_t._row_num > ?")
		}
	} else if limit > 0 {
		// _row_num <= limit
		sb.WriteString("_t._row_num <= ?")
	}

	sb.WriteByte(')')

	return sb.String()
}

func appendAdditionalJoinOnConditions(
	gen schema.QueryGen, b []byte, conditions []schema.QueryWithArgs,
) []byte {
	// Smart buffer handling: if buffer is empty, create and pre-allocate
	if len(b) == 0 {
		estimatedSize := len(conditions) * 60
		b = make([]byte, 0, estimatedSize)
	}

	for i, cond := range conditions {
		if i > 0 {
			b = append(b, " AND "...)
		}
		b = gen.AppendQuery(b, cond.Query, cond.Args...)
	}
	return b
}


