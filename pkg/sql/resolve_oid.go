// Copyright 2021 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package sql

import (
	"bytes"
	"context"
	"fmt"

	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/isql"
	"github.com/cockroachdb/cockroach/pkg/sql/lexbase"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgcode"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sessiondata"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/errors"
	"github.com/lib/pq/oid"
)

// ResolveOIDFromString is part of tree.TypeResolver.
func (p *planner) ResolveOIDFromString(
	ctx context.Context, resultType *types.T, toResolve *tree.DString,
) (_ *tree.DOid, errSafeToIgnore bool, _ error) {
	return resolveOID(
		ctx, p.Txn(),
		p.InternalSQLTxn(),
		resultType, toResolve,
	)
}

// ResolveOIDFromOID is part of tree.TypeResolver.
func (p *planner) ResolveOIDFromOID(
	ctx context.Context, resultType *types.T, toResolve *tree.DOid,
) (_ *tree.DOid, errSafeToIgnore bool, _ error) {
	return resolveOID(
		ctx, p.Txn(),
		p.InternalSQLTxn(),
		resultType, toResolve,
	)
}

func resolveOID(
	ctx context.Context, txn *kv.Txn, ie isql.Executor, resultType *types.T, toResolve tree.Datum,
) (_ *tree.DOid, errSafeToIgnore bool, _ error) {
	info, ok := regTypeInfos[resultType.Oid()]
	if !ok {
		return nil, true, pgerror.Newf(
			pgcode.InvalidTextRepresentation,
			"invalid input syntax for type %s: %q",
			resultType,
			tree.AsStringWithFlags(toResolve, tree.FmtBareStrings),
		)
	}
	queryCol := info.nameCol
	_, isOID := toResolve.(*tree.DOid)
	if isOID {
		queryCol = "oid"
		// PostgreSQL's regtype output uses SQL-standard builtin type names,
		// rather than the internal names stored in pg_type (for example,
		// "integer" instead of "int4").
		if resultType.Oid() == oid.T_regtype {
			o := tree.MustBeDOid(toResolve).Oid
			if typ, ok := types.OidToType[o]; ok {
				return tree.NewDOidWithTypeAndName(o, resultType, typ.SQLStandardName()), true, nil
			}
		}
	}
	q := fmt.Sprintf("SELECT %[1]s.oid, %[1]s.%[2]s FROM pg_catalog.%[1]s WHERE %[3]s = $1",
		info.tableName, info.nameCol, queryCol)
	if info.namespaceCol != "" {
		// PostgreSQL emits a schema-qualified reg* name exactly when resolving
		// the unqualified name through search_path would select a different
		// schema. The search is by name, not OID, so overloaded functions in an
		// earlier schema shadow every overload of that name in a later schema.
		q = fmt.Sprintf(`
SELECT obj.oid, obj.%[2]s, n.nspname,
       (
         SELECT n2.nspname
           FROM pg_catalog.%[1]s AS obj2
           JOIN pg_catalog.pg_namespace AS n2 ON obj2.%[4]s = n2.oid
           JOIN unnest(current_schemas(true)) WITH ORDINALITY AS path(nspname, ordinality)
             ON n2.nspname = path.nspname
          WHERE obj2.%[2]s = obj.%[2]s
          ORDER BY path.ordinality
          LIMIT 1
       )
  FROM pg_catalog.%[1]s AS obj
  JOIN pg_catalog.pg_namespace AS n ON obj.%[4]s = n.oid
 WHERE obj.%[3]s = $1`,
			info.tableName, info.nameCol, queryCol, info.namespaceCol)
	}

	results, err := ie.QueryRowEx(ctx, "queryOid", txn,
		sessiondata.NoSessionDataOverride, q, toResolve)
	if err != nil {
		if catalog.HasInactiveDescriptorError(err) {
			// Descriptor is either dropped or offline, so
			// the OID does not exist.
			return nil, true, pgerror.Newf(info.errType,
				"%s %s does not exist", info.objName, toResolve)
		} else if errors.HasType(err, (*tree.MultipleResultsError)(nil)) {
			return nil, false, pgerror.Newf(pgcode.AmbiguousAlias,
				"more than one %s named %s", info.objName, toResolve)
		}
		return nil, false, err
	}
	if results.Len() == 0 {
		return nil, true, pgerror.Newf(info.errType,
			"%s %s does not exist", info.objName, toResolve)
	}
	name := tree.AsStringWithFlags(results[1], tree.FmtBareStrings)
	if info.namespaceCol != "" && (results[3] == tree.DNull ||
		tree.AsStringWithFlags(results[2], tree.FmtBareStrings) !=
			tree.AsStringWithFlags(results[3], tree.FmtBareStrings)) {
		name = quoteQualifiedIdentifier(
			tree.AsStringWithFlags(results[2], tree.FmtBareStrings), name,
		)
	}
	return tree.NewDOidWithTypeAndName(results[0].(*tree.DOid).Oid, resultType, name), true, nil
}

// quoteQualifiedIdentifier formats a schema-qualified identifier the same way
// PostgreSQL's quote_qualified_identifier does.
func quoteQualifiedIdentifier(schema, name string) string {
	var buf bytes.Buffer
	lexbase.EncodeRestrictedSQLIdent(&buf, schema, lexbase.EncNoFlags)
	buf.WriteByte('.')
	lexbase.EncodeRestrictedSQLIdent(&buf, name, lexbase.EncNoFlags)
	return buf.String()
}

// regTypeInfo contains details on a pg_catalog table that has a reg* type.
type regTypeInfo struct {
	tableName string
	// nameCol is the name of the column that contains the table's entity name.
	nameCol string
	// objName is a human-readable name describing the objects in the table.
	objName string
	// errType is the pg error code in case the object does not exist.
	errType pgcode.Code
	// namespaceCol is the column containing the object's schema OID. An empty
	// value indicates that the object is not schema-scoped.
	namespaceCol string
}

// regTypeInfos maps an oid.Oid to a regTypeInfo that describes the pg_catalog
// table that contains the entities of the type of the key.
var regTypeInfos = map[oid.Oid]regTypeInfo{
	oid.T_regclass:     {"pg_class", "relname", "relation", pgcode.UndefinedTable, "relnamespace"},
	oid.T_regnamespace: {"pg_namespace", "nspname", "namespace", pgcode.UndefinedObject, ""},
	oid.T_regproc:      {"pg_proc", "proname", "function", pgcode.UndefinedFunction, "pronamespace"},
	oid.T_regprocedure: {"pg_proc", "proname", "function", pgcode.UndefinedFunction, "pronamespace"},
	oid.T_regrole:      {"pg_authid", "rolname", "role", pgcode.UndefinedObject, ""},
	oid.T_regtype:      {"pg_type", "typname", "type", pgcode.UndefinedObject, "typnamespace"},
}
