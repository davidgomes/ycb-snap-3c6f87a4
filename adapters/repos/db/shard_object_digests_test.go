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
	"fmt"
	"testing"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/require"

	routerTypes "github.com/weaviate/weaviate/cluster/router/types"
)

// TestShardDigestScansLargeObjects runs ObjectDigestsInRange and CompareDigests
// over objects with large vectors spread across two segments and the memtable,
// in both segment read modes. The scans only read object headers from disk, and
// must still report exactly the newest version of every live object.
func TestShardDigestScansLargeObjects(t *testing.T) {
	ctx := context.Background()
	const (
		class = "DigestScanLargeObjectsTest"
		dims  = 1536
		n     = 20
	)

	modes := []struct {
		name string
		opt  func(*Index)
	}{
		{"mmap", func(*Index) {}},
		{"pread", func(idx *Index) {
			idx.Config.AvoidMMap = true
			idx.Config.MinMMapSize = 0
		}},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			sl, _ := testShard(t, ctx, class, mode.opt)
			s := concreteShard(t, sl)

			ids := make([]strfmt.UUID, n)
			for i := range ids {
				ids[i] = strfmt.UUID(fmt.Sprintf("%08x-0000-0000-0000-000000000000", i+1))
			}

			newest := map[strfmt.UUID]int64{}
			put := func(id strfmt.UUID, updateTime int64) {
				obj := testObjWithTime(class, id, updateTime)
				obj.Vector = make([]float32, dims)
				for d := range obj.Vector {
					obj.Vector[d] = float32(updateTime) + float32(d)
				}
				require.NoError(t, sl.PutObject(ctx, obj))
				newest[id] = updateTime
			}

			for i, id := range ids {
				put(id, int64(1_000+i))
			}
			flushShard(t, ctx, sl)
			for i := 0; i < n; i += 3 {
				put(ids[i], int64(5_000+i))
			}
			flushShard(t, ctx, sl)
			for i := 1; i < n; i += 4 {
				put(ids[i], int64(9_000+i))
			}
			for _, i := range []int{2, 7, 9} {
				require.NoError(t, sl.DeleteObject(ctx, ids[i], time.UnixMilli(20_000)))
				delete(newest, ids[i])
			}

			t.Run("ObjectDigestsInRange", func(t *testing.T) {
				for _, limit := range []int{1, 4, n} {
					got := map[strfmt.UUID]int64{}
					var order []strfmt.UUID
					from := strfmt.UUID("00000000-0000-0000-0000-000000000000")
					for {
						digests, err := s.ObjectDigestsInRange(ctx, from, "ffffffff-ffff-ffff-ffff-ffffffffffff", limit)
						require.NoError(t, err)
						if len(digests) == 0 {
							break
						}
						for _, d := range digests {
							got[strfmt.UUID(d.ID)] = d.UpdateTime
							order = append(order, strfmt.UUID(d.ID))
						}
						last, err := bytesFromUUID(strfmt.UUID(digests[len(digests)-1].ID))
						require.NoError(t, err)
						require.False(t, incToNextLexValue(last))
						from = strfmt.UUID(fmt.Sprintf("%x-%x-%x-%x-%x", last[0:4], last[4:6], last[6:8], last[8:10], last[10:16]))
					}
					require.Equal(t, newest, got, "limit=%d", limit)
					require.Len(t, order, len(newest), "limit=%d: every live object exactly once", limit)
					for i := 1; i < len(order); i++ {
						require.Less(t, string(order[i-1]), string(order[i]), "limit=%d: digests in UUID order", limit)
					}
				}
			})

			t.Run("CompareDigests", func(t *testing.T) {
				newer := make([]routerTypes.RepairResponse, n)
				equal := make([]routerTypes.RepairResponse, n)
				for i, id := range ids {
					newer[i] = routerTypes.RepairResponse{ID: string(id), UpdateTime: 1_000_000}
					equal[i] = routerTypes.RepairResponse{ID: string(id), UpdateTime: max(newest[id], 1)}
				}

				out, err := s.CompareDigests(ctx, newer)
				require.NoError(t, err)
				require.Len(t, out, n, "a newer source is stale-on-target (live) or missing (deleted) for every id")
				for i, r := range out {
					require.Equal(t, string(ids[i]), r.ID)
					require.Equal(t, newest[ids[i]], r.UpdateTime, "local update time, or 0 when deleted: %s", r.ID)
				}

				out, err = s.CompareDigests(ctx, equal)
				require.NoError(t, err)
				require.Len(t, out, n-len(newest), "only deleted ids are reported when timestamps match")
				for _, r := range out {
					_, live := newest[strfmt.UUID(r.ID)]
					require.False(t, live, "live id %s with an equal timestamp must not be reported", r.ID)
					require.Zero(t, r.UpdateTime)
				}
			})
		})
	}
}
