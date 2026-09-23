//                           _       _
// __      _____  __ ___   ___  __ _| |_ ___
// \ \ /\ / / _ \/ _` \ \ / / |/ _` | __/ _ \
//  \ V  V /  __/ (_| |\ V /| | (_| | ||  __/
//   \_/\_/ \___|\__,_| \_/ |_|\__,_|\__\___|
//
//  Copyright © 2016 - 2026 Weaviate B.V. All rights reserved.
//
//  CONTACT: hello@weaviate.io
//

//go:build integrationTest

package db

import (
	"bytes"
	"context"
	"sort"
	"testing"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/weaviate/weaviate/adapters/repos/db/helpers"
	routerTypes "github.com/weaviate/weaviate/cluster/router/types"
	"github.com/weaviate/weaviate/entities/storobj"
)

// TestShardDigestScansOverSegments verifies ObjectDigestsInRange,
// CompareDigests and ApplyToObjectDigests, which read only the object header
// from disk segments, against objects with large vectors spread across
// several flushed segments (with updates and deletes) and the memtable.
func TestShardDigestScansOverSegments(t *testing.T) {
	ctx := context.Background()
	const class = "DigestScansOverSegmentsTest"
	const n = 60

	ids := make([]strfmt.UUID, n)
	for i := range ids {
		ids[i] = strfmt.UUID(uuid.New().String())
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := uuid.MustParse(ids[i].String()), uuid.MustParse(ids[j].String())
		return bytes.Compare(a[:], b[:]) < 0
	})

	vector := make([]float32, 1536)
	for i := range vector {
		vector[i] = float32(i)
	}
	objWithVector := func(id strfmt.UUID, ts int64) *storobj.Object {
		obj := testObjWithTime(class, id, ts)
		obj.Vector = vector
		return obj
	}

	sl, _ := testShard(t, ctx, class)
	s := concreteShard(t, sl)

	expected := map[strfmt.UUID]int64{}

	// segment 1: all objects
	for i, id := range ids {
		ts := int64(1_000 + i)
		require.NoError(t, sl.PutObject(ctx, objWithVector(id, ts)))
		expected[id] = ts
	}
	flushShard(t, ctx, sl)

	// segment 2: updates and deletes
	for i := 0; i < n; i += 3 {
		ts := int64(5_000 + i)
		require.NoError(t, sl.PutObject(ctx, objWithVector(ids[i], ts)))
		expected[ids[i]] = ts
	}
	for i := 1; i < n; i += 7 {
		require.NoError(t, sl.DeleteObject(ctx, ids[i], time.UnixMilli(9_000)))
		delete(expected, ids[i])
	}
	flushShard(t, ctx, sl)

	// memtable: further updates, a resurrection and a delete
	for i := 0; i < n; i += 5 {
		ts := int64(10_000 + i)
		require.NoError(t, sl.PutObject(ctx, objWithVector(ids[i], ts)))
		expected[ids[i]] = ts
	}
	require.NoError(t, sl.DeleteObject(ctx, ids[2], time.UnixMilli(20_000)))
	delete(expected, ids[2])

	var want []routerTypes.RepairResponse
	for _, id := range ids {
		if ts, ok := expected[id]; ok {
			want = append(want, routerTypes.RepairResponse{ID: id.String(), UpdateTime: ts})
		}
	}
	require.NotEmpty(t, want)

	t.Run("ObjectDigestsInRange", func(t *testing.T) {
		got, err := s.ObjectDigestsInRange(ctx, strfmt.UUID(uuid.Nil.String()),
			strfmt.UUID("ffffffff-ffff-ffff-ffff-ffffffffffff"), n*2)
		require.NoError(t, err)
		require.Equal(t, want, got)

		// limit and sub-range
		got, err = s.ObjectDigestsInRange(ctx, strfmt.UUID(want[3].ID), strfmt.UUID(want[10].ID), 5)
		require.NoError(t, err)
		require.Equal(t, want[3:8], got)
	})

	t.Run("CompareDigests", func(t *testing.T) {
		src := make([]routerTypes.RepairResponse, 0, n)
		for _, id := range ids {
			src = append(src, routerTypes.RepairResponse{ID: id.String(), UpdateTime: 1 << 40})
		}
		got, err := s.CompareDigests(ctx, src)
		require.NoError(t, err)
		require.Len(t, got, n)
		for i, r := range got {
			require.Equal(t, ids[i].String(), r.ID)
			require.Equal(t, expected[ids[i]], r.UpdateTime, "id %s", r.ID)
		}
	})

	t.Run("ApplyToObjectDigests", func(t *testing.T) {
		got := map[strfmt.UUID]int64{}
		err := s.store.Bucket(helpers.ObjectsBucketLSM).ApplyToObjectDigests(ctx, func() {},
			func(uuidBytes []byte, updateTime int64) error {
				u, err := uuid.FromBytes(uuidBytes)
				require.NoError(t, err)
				id := strfmt.UUID(u.String())
				prev, seen := got[id]
				require.False(t, seen, "id %s visited twice (%d, then %d; expected %d)", id, prev, updateTime, expected[id])
				got[id] = updateTime
				return nil
			})
		require.NoError(t, err)
		require.Equal(t, expected, got)
	})
}
