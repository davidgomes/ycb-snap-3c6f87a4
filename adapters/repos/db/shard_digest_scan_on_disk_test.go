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
	"context"
	"sort"
	"testing"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/weaviate/weaviate/adapters/repos/db/helpers"
	routerTypes "github.com/weaviate/weaviate/cluster/router/types"
	"github.com/weaviate/weaviate/entities/storobj"
)

// TestShardDigestScansOnDiskLargeVectors verifies that the digest scans
// (ObjectDigestsInRange, CompareDigests, ApplyToObjectDigests) report the
// correct update times when objects carry large vectors and are spread across
// several flushed segments, tombstones and the active memtable. These scans
// only load the object header from disk segments.
func TestShardDigestScansOnDiskLargeVectors(t *testing.T) {
	ctx := context.Background()
	const (
		class = "DigestScanOnDiskTest"
		n     = 60
		dims  = 1536
	)

	ids := make([]strfmt.UUID, n)
	for i := range ids {
		var u uuid.UUID
		u[0] = byte(i * 4)
		u[15] = byte(i + 1)
		ids[i] = strfmt.UUID(u.String())
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	objWithVector := func(id strfmt.UUID, ts int64, seed int) *storobj.Object {
		obj := testObjWithTime(class, id, ts)
		obj.Vector = make([]float32, dims)
		for i := range obj.Vector {
			obj.Vector[i] = float32(seed*dims + i)
		}
		return obj
	}

	sl, _ := testShard(t, ctx, class)
	s := concreteShard(t, sl)

	expected := make(map[strfmt.UUID]int64, n)

	for i, id := range ids {
		ts := int64(1_000 + i)
		require.NoError(t, sl.PutObject(ctx, objWithVector(id, ts, i)))
		expected[id] = ts
	}
	flushShard(t, ctx, sl)

	for i := 0; i < n; i += 3 {
		ts := int64(5_000 + i)
		require.NoError(t, sl.PutObject(ctx, objWithVector(ids[i], ts, i+n)))
		expected[ids[i]] = ts
	}
	flushShard(t, ctx, sl)

	for i := 0; i < n; i += 5 {
		require.NoError(t, sl.DeleteObject(ctx, ids[i], time.UnixMilli(20_000)))
		delete(expected, ids[i])
	}
	flushShard(t, ctx, sl)

	// left in the memtable
	for i := 0; i < n; i += 7 {
		ts := int64(9_000 + i)
		require.NoError(t, sl.PutObject(ctx, objWithVector(ids[i], ts, i+2*n)))
		expected[ids[i]] = ts
	}
	// deletes of on-disk objects left in the memtable
	for i := 1; i < n; i += 13 {
		require.NoError(t, sl.DeleteObject(ctx, ids[i], time.UnixMilli(30_000)))
		delete(expected, ids[i])
	}

	bucket := s.store.Bucket(helpers.ObjectsBucketLSM)
	onDisk := bucket.CursorOnDisk()
	k, v := onDisk.First()
	require.NotNil(t, k, "expected flushed segments")
	require.Greater(t, len(v), dims*4, "expected on-disk values to carry the vector")
	onDisk.Close()

	var expectedSorted []routerTypes.RepairResponse
	for _, id := range ids {
		if ts, ok := expected[id]; ok {
			expectedSorted = append(expectedSorted, routerTypes.RepairResponse{ID: string(id), UpdateTime: ts})
		}
	}

	t.Run("ObjectDigestsInRange full range", func(t *testing.T) {
		got, err := s.ObjectDigestsInRange(ctx,
			"00000000-0000-0000-0000-000000000000", "ffffffff-ffff-ffff-ffff-ffffffffffff", 10*n)
		require.NoError(t, err)
		assert.Equal(t, expectedSorted, got)
	})

	t.Run("ObjectDigestsInRange paged", func(t *testing.T) {
		var got []routerTypes.RepairResponse
		from := strfmt.UUID("00000000-0000-0000-0000-000000000000")
		for {
			page, err := s.ObjectDigestsInRange(ctx, from, "ffffffff-ffff-ffff-ffff-ffffffffffff", 7)
			require.NoError(t, err)
			if len(page) == 0 {
				break
			}
			if len(got) > 0 && page[0].ID == got[len(got)-1].ID {
				page = page[1:]
			}
			if len(page) == 0 {
				break
			}
			got = append(got, page...)
			from = strfmt.UUID(page[len(page)-1].ID)
		}
		assert.Equal(t, expectedSorted, got)
	})

	t.Run("CompareDigests", func(t *testing.T) {
		probe := make([]routerTypes.RepairResponse, len(ids))
		for i, id := range ids {
			probe[i] = routerTypes.RepairResponse{ID: string(id), UpdateTime: 1 << 40}
		}
		got, err := s.CompareDigests(ctx, probe)
		require.NoError(t, err)
		require.Len(t, got, len(ids))
		for i, id := range ids {
			assert.Equal(t, string(id), got[i].ID)
			assert.Equal(t, expected[id], got[i].UpdateTime, "update time for %s", id)
		}
	})

	t.Run("ApplyToObjectDigests", func(t *testing.T) {
		got := map[strfmt.UUID]int64{}
		err := bucket.ApplyToObjectDigests(ctx, func() {}, func(uuidBytes []byte, updateTime int64) error {
			u, err := uuid.FromBytes(uuidBytes)
			require.NoError(t, err)
			_, dup := got[strfmt.UUID(u.String())]
			require.False(t, dup, "object %s visited twice", u)
			got[strfmt.UUID(u.String())] = updateTime
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, expected, got)
	})
}
