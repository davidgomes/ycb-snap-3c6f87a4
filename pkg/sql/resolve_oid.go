// Copyright 2021 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package sql

import (
	"context"
	"fmt"

	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/isql"
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
	if resultType.Oid() == oid.T_regtype && types.IsOIDUserDefinedType(toResolve.Oid) {
		typ, err := p.ResolveTypeByOID(ctx, toResolve.Oid)
		if err != nil {
			return nil, false, err
		}
		if typ.TypeMeta.Name != nil {
			typeName := typ.TypeMeta.Name
			displayName := tree.NameString(typeName.Name)
			resolved, err := p.GetTypeFromValidSQLSyntax(ctx, displayName)
			if err != nil || resolved.Oid() != toResolve.Oid {
				displayName = tree.NameString(typeName.Schema) + "." + displayName
			}
			return tree.NewDOidWithTypeAndName(
				toResolve.Oid, resultType, displayName,
			), true, nil
		}
	}
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
	if _, isOid := toResolve.(*tree.DOid); isOid {
		queryCol = "oid"
	}
	var q string
	if info.namespaceCol == "" {
		q = fmt.Sprintf(
			"SELECT %[1]s.oid, %[2]s FROM pg_catalog.%[1]s WHERE %[3]s = $1",
			info.tableName, info.nameCol, queryCol,
		)
	} else {
		q = fmt.Sprintf(
			`SELECT obj.oid,
			        CASE WHEN pg_catalog.%[1]s(obj.oid)
			             THEN pg_catalog.quote_ident(obj.%[2]s)
			             ELSE pg_catalog.quote_ident(n.nspname) || '.' ||
			                  pg_catalog.quote_ident(obj.%[2]s)
			        END
			   FROM pg_catalog.%[3]s AS obj
			   JOIN pg_catalog.pg_namespace AS n ON obj.%[4]s = n.oid
			  WHERE obj.%[5]s = $1`,
			info.visibilityFn, info.nameCol, info.tableName, info.namespaceCol, queryCol,
		)
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
	return tree.NewDOidWithTypeAndName(
		results[0].(*tree.DOid).Oid,
		resultType,
		string(tree.MustBeDString(results[1])),
	), true, nil
}

// regTypeInfo contains details on a pg_catalog table that has a reg* type.
type regTypeInfo struct {
	tableName string
	// nameCol is the name of the column that contains the table's entity name.
	nameCol string
	// namespaceCol is the name of the column that contains the entity's schema
	// OID. It is empty for reg* types whose entities are not schema-qualified.
	namespaceCol string
	// visibilityFn is the PostgreSQL compatibility builtin that determines
	// whether the entity can be referenced without a schema qualifier.
	visibilityFn string
	// objName is a human-readable name describing the objects in the table.
	objName string
	// errType is the pg error code in case the object does not exist.
	errType pgcode.Code
}

// regTypeInfos maps an oid.Oid to a regTypeInfo that describes the pg_catalog
// table that contains the entities of the type of the key.
var regTypeInfos = map[oid.Oid]regTypeInfo{
	oid.T_regclass: {
		"pg_class", "relname", "relnamespace", "pg_table_is_visible",
		"relation", pgcode.UndefinedTable,
	},
	oid.T_regnamespace: {
		"pg_namespace", "nspname", "", "", "namespace", pgcode.UndefinedObject,
	},
	oid.T_regproc: {
		"pg_proc", "proname", "pronamespace", "pg_function_is_visible",
		"function", pgcode.UndefinedFunction,
	},
	oid.T_regprocedure: {
		"pg_proc", "proname", "pronamespace", "pg_function_is_visible",
		"function", pgcode.UndefinedFunction,
	},
	oid.T_regrole: {
		"pg_authid", "rolname", "", "", "role", pgcode.UndefinedObject,
	},
	oid.T_regtype: {
		"pg_type", "typname", "typnamespace", "pg_type_is_visible",
		"type", pgcode.UndefinedObject,
	},
}
