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
	return resolveOID(
		ctx, p.Txn(),
		p.InternalSQLTxn(),
		resultType, toResolve,
	)
}

// QualifyRegObjectName is part of eval.Planner. See the comment there for
// details.
func (p *planner) QualifyRegObjectName(
	ctx context.Context, regTypeOid oid.Oid, schemaName string, name string,
) (string, error) {
	info, ok := regTypeInfos[regTypeOid]
	if !ok || info.nsCol == "" {
		// This reg* family has no notion of schema, so the name is never
		// qualified.
		return quoteRegIdent(name), nil
	}
	return qualifyRegObjectName(ctx, p.Txn(), p.InternalSQLTxn(), info, schemaName, name)
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
	_, isOid := toResolve.(*tree.DOid)
	queryCol := info.nameCol
	if isOid {
		queryCol = "oid"
	}
	// When resolving a name for a given OID (i.e. producing a display name,
	// as opposed to looking up the OID for a given name), and the reg* family
	// has a notion of schema, also fetch the entity's namespace so that we
	// can determine below whether the bare name needs to be schema-qualified
	// to match Postgres's visibility rules.
	needsQualification := isOid && info.nsCol != ""
	var q string
	if needsQualification {
		q = fmt.Sprintf(
			`SELECT %[1]s.oid, %[1]s.%[2]s, ns.nspname
			 FROM pg_catalog.%[1]s
			 JOIN pg_catalog.pg_namespace AS ns ON %[1]s.%[3]s = ns.oid
			 WHERE %[1]s.oid = $1`,
			info.tableName, info.nameCol, info.nsCol,
		)
	} else {
		q = fmt.Sprintf(
			"SELECT %[1]s.oid, %[1]s.%[2]s FROM pg_catalog.%[1]s WHERE %[3]s = $1",
			info.tableName, info.nameCol, queryCol,
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
	name := tree.AsStringWithFlags(results[1], tree.FmtBareStrings)
	if needsQualification {
		schemaName := tree.AsStringWithFlags(results[2], tree.FmtBareStrings)
		name, err = qualifyRegObjectName(ctx, txn, ie, info, schemaName, name)
		if err != nil {
			return nil, false, err
		}
	}
	return tree.NewDOidWithTypeAndName(
		results[0].(*tree.DOid).Oid,
		resultType,
		name,
	), true, nil
}

// qualifyRegObjectName returns the Postgres quote_qualified_identifier-style
// display name for the object named name that lives in namespace
// schemaName, for the reg* family described by info (identified by its
// pg_catalog table, name column, and the FK column into pg_namespace).
//
// Following Postgres, the bare (quoted) name is returned unless it would not
// resolve back to the same object via the current search_path -- that is,
// unless schemaName is the first schema on the search_path that contains any
// entity named name in info.tableName. When another, earlier schema also has
// an entity of that name (shadowing this one) or schemaName is not on the
// search_path at all, the name is schema-qualified instead. Each identifier
// component is quoted only when necessary.
func qualifyRegObjectName(
	ctx context.Context, txn *kv.Txn, ie isql.Executor, info regTypeInfo, schemaName, name string,
) (string, error) {
	q := fmt.Sprintf(
		`SELECT COALESCE($1 = (
			SELECT ns.nspname
			FROM pg_catalog.%[1]s AS o
			JOIN pg_catalog.pg_namespace AS ns ON o.%[2]s = ns.oid
			WHERE o.%[3]s = $2
			  AND ns.nspname = ANY (current_schemas(true))
			ORDER BY array_position(current_schemas(true), ns.nspname)
			LIMIT 1
		), false)`,
		info.tableName, info.nsCol, info.nameCol,
	)
	row, err := ie.QueryRowEx(
		ctx, "qualify-reg-object-name", txn, sessiondata.NoSessionDataOverride, q,
		schemaName, name,
	)
	if err != nil {
		return "", err
	}
	if row.Len() != 0 && bool(tree.MustBeDBool(row[0])) {
		return quoteRegIdent(name), nil
	}
	return quoteRegIdent(schemaName) + "." + quoteRegIdent(name), nil
}

// quoteRegIdent quotes an identifier component (schema or object name) for
// display, only when necessary, matching Postgres's quote_identifier /
// quote_qualified_identifier behavior.
func quoteRegIdent(s string) string {
	n := tree.Name(s)
	return n.String()
}

// regTypeInfo contains details on a pg_catalog table that has a reg* type.
type regTypeInfo struct {
	tableName string
	// nameCol is the name of the column that contains the table's entity name.
	nameCol string
	// nsCol is the name of the column that is a foreign key reference into
	// pg_namespace(oid), identifying the schema the entity lives in. It is
	// empty for reg* families that have no notion of schema (regnamespace,
	// regrole); such names are never schema-qualified.
	nsCol string
	// objName is a human-readable name describing the objects in the table.
	objName string
	// errType is the pg error code in case the object does not exist.
	errType pgcode.Code
}

// regTypeInfos maps an oid.Oid to a regTypeInfo that describes the pg_catalog
// table that contains the entities of the type of the key.
var regTypeInfos = map[oid.Oid]regTypeInfo{
	oid.T_regclass:     {"pg_class", "relname", "relnamespace", "relation", pgcode.UndefinedTable},
	oid.T_regnamespace: {"pg_namespace", "nspname", "", "namespace", pgcode.UndefinedObject},
	oid.T_regproc:      {"pg_proc", "proname", "pronamespace", "function", pgcode.UndefinedFunction},
	oid.T_regprocedure: {"pg_proc", "proname", "pronamespace", "function", pgcode.UndefinedFunction},
	oid.T_regrole:      {"pg_authid", "rolname", "", "role", pgcode.UndefinedObject},
	oid.T_regtype:      {"pg_type", "typname", "typnamespace", "type", pgcode.UndefinedObject},
}
