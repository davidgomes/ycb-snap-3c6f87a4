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

package lsmkv

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/cyclemanager"
	"github.com/weaviate/weaviate/entities/storobj"
)

// digestObjectValue builds a value whose header DocIDAndTimeFromBinary can
// parse, followed by a large payload standing in for vectors.
func digestObjectValue(docID uint64, updateTime int64) []byte {
	v := make([]byte, storobj.MarshallerV1HeaderLen+6_000)
	v[0] = 1
	binary.LittleEndian.PutUint64(v[1:9], docID)
	binary.LittleEndian.PutUint64(v[storobj.MarshallerV1HeaderLen-8:storobj.MarshallerV1HeaderLen], uint64(updateTime))
	for i := storobj.MarshallerV1HeaderLen; i < len(v); i++ {
		v[i] = byte(i)
	}
	return v
}

func TestBucketApplyToObjectDigests(t *testing.T) {
	type op struct {
		key    string
		docID  uint64
		time   int64
		delete bool
		flush  bool // flush the memtable after this op
	}
	put := func(key string, docID uint64, ts int64) op { return op{key: key, docID: docID, time: ts} }
	del := func(key string) op { return op{key: key, delete: true} }
	flush := op{flush: true}

	tests := []struct {
		name string
		ops  []op
		want map[string]int64
	}{
		{
			name: "disk only",
			ops:  []op{put("a", 1, 10), put("b", 2, 20), flush},
			want: map[string]int64{"a": 10, "b": 20},
		},
		{
			name: "memtable only",
			ops:  []op{put("a", 1, 10), put("b", 2, 20)},
			want: map[string]int64{"a": 10, "b": 20},
		},
		{
			name: "memtable update with new docID shadows disk",
			ops:  []op{put("a", 1, 10), put("b", 2, 20), flush, put("a", 3, 30)},
			want: map[string]int64{"a": 30, "b": 20},
		},
		{
			name: "memtable update with preserved docID shadows disk",
			ops:  []op{put("a", 1, 10), flush, put("a", 1, 30)},
			want: map[string]int64{"a": 30},
		},
		{
			name: "memtable tombstone shadows live disk entry",
			ops:  []op{put("a", 1, 10), put("b", 2, 20), flush, del("a")},
			want: map[string]int64{"b": 20},
		},
		{
			name: "resurrection in memtable after disk tombstone",
			ops:  []op{put("a", 1, 10), flush, del("a"), flush, put("a", 5, 50)},
			want: map[string]int64{"a": 50},
		},
		{
			name: "delete and re-put within memtable over disk",
			ops:  []op{put("a", 1, 10), flush, del("a"), put("a", 6, 60)},
			want: map[string]int64{"a": 60},
		},
		{
			name: "multi-segment update chain then memtable update",
			ops: []op{
				put("a", 1, 10), put("b", 2, 20), put("c", 3, 30), flush,
				put("a", 4, 40), del("b"), flush,
				put("b", 5, 50), put("c", 6, 60),
			},
			want: map[string]int64{"a": 40, "b": 50, "c": 60},
		},
		{
			name: "memtable docID reused by a different key",
			ops:  []op{put("a", 1, 10), put("b", 2, 20), flush, put("b", 1, 30)},
			want: map[string]int64{"a": 10, "b": 30},
		},
	}

	for _, pread := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("pread=%v/%s", pread, tt.name), func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				b, err := NewBucketCreator().NewBucket(ctx, dir, dir, nullLogger(), nil,
					cyclemanager.NewCallbackGroupNoop(), cyclemanager.NewCallbackGroupNoop(),
					WithStrategy(StrategyReplace), WithPread(pread))
				require.NoError(t, err)
				defer b.Shutdown(ctx)
				b.SetMemtableThreshold(1e9)

				for _, o := range tt.ops {
					switch {
					case o.flush:
						require.NoError(t, b.FlushAndSwitch())
					case o.delete:
						require.NoError(t, b.Delete([]byte(o.key)))
					default:
						require.NoError(t, b.Put([]byte(o.key), digestObjectValue(o.docID, o.time)))
					}
				}

				got := map[string]int64{}
				afterInMemCalls := 0
				err = b.ApplyToObjectDigests(ctx, func() { afterInMemCalls++ },
					func(k []byte, updateTime int64) error {
						prev, seen := got[string(k)]
						require.False(t, seen, "key %q visited twice (%d, then %d)", k, prev, updateTime)
						got[string(k)] = updateTime
						return nil
					})
				require.NoError(t, err)
				require.Equal(t, 1, afterInMemCalls)
				require.Equal(t, tt.want, got)
			})
		}
	}
}
