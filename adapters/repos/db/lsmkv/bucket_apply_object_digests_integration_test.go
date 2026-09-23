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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/storobj"
)

// objectDigestTestValue builds a value whose header DocIDAndTimeFromBinary can
// parse, followed by a large payload standing in for vectors/properties.
func objectDigestTestValue(docID uint64, updateTime int64, payload int) []byte {
	v := make([]byte, storobj.MarshallerV1HeaderLen+payload)
	v[0] = 1
	binary.LittleEndian.PutUint64(v[1:9], docID)
	binary.LittleEndian.PutUint64(v[storobj.MarshallerV1HeaderLen-8:storobj.MarshallerV1HeaderLen], uint64(updateTime))
	for i := storobj.MarshallerV1HeaderLen; i < len(v); i++ {
		v[i] = byte(i)
	}
	return v
}

type objectDigestOp struct {
	kind   string // "put", "delete", "flush"
	key    string
	docID  uint64
	update int64
}

func odPut(key string, docID uint64, update int64) objectDigestOp {
	return objectDigestOp{kind: "put", key: key, docID: docID, update: update}
}

func odDelete(key string) objectDigestOp { return objectDigestOp{kind: "delete", key: key} }

var odFlush = objectDigestOp{kind: "flush"}

func TestBucketApplyToObjectDigests(t *testing.T) {
	tests := []struct {
		name     string
		ops      []objectDigestOp
		expected map[string]int64
	}{
		{
			name:     "memtable only",
			ops:      []objectDigestOp{odPut("a", 1, 10), odPut("b", 2, 20)},
			expected: map[string]int64{"a": 10, "b": 20},
		},
		{
			name:     "disk only across segments",
			ops:      []objectDigestOp{odPut("a", 1, 10), odFlush, odPut("b", 2, 20), odFlush, odPut("a", 3, 30), odFlush},
			expected: map[string]int64{"a": 30, "b": 20},
		},
		{
			name:     "memtable update with new docID shadows disk",
			ops:      []objectDigestOp{odPut("a", 1, 10), odPut("b", 2, 20), odFlush, odPut("a", 3, 30)},
			expected: map[string]int64{"a": 30, "b": 20},
		},
		{
			name:     "memtable update with same docID shadows disk",
			ops:      []objectDigestOp{odPut("a", 1, 10), odFlush, odPut("a", 1, 30)},
			expected: map[string]int64{"a": 30},
		},
		{
			name:     "memtable tombstone hides disk object",
			ops:      []objectDigestOp{odPut("a", 1, 10), odPut("b", 2, 20), odFlush, odDelete("a")},
			expected: map[string]int64{"b": 20},
		},
		{
			name:     "disk tombstone then memtable re-put",
			ops:      []objectDigestOp{odPut("a", 1, 10), odFlush, odDelete("a"), odFlush, odPut("a", 5, 50)},
			expected: map[string]int64{"a": 50},
		},
		{
			name:     "disk tombstone stays hidden",
			ops:      []objectDigestOp{odPut("a", 1, 10), odFlush, odDelete("a"), odFlush},
			expected: map[string]int64{},
		},
		{
			name: "memtable docID equal to unrelated disk key does not hide it",
			ops:  []objectDigestOp{odPut("b", 7, 20), odFlush, odPut("a", 7, 10)},
			expected: map[string]int64{
				"a": 10,
				"b": 20,
			},
		},
		{
			name: "multi round put update delete re-put",
			ops: []objectDigestOp{
				odPut("a", 1, 10), odPut("b", 2, 20), odPut("c", 3, 30), odFlush,
				odPut("a", 4, 40), odDelete("b"), odFlush,
				odDelete("a"), odPut("b", 5, 50), odPut("d", 6, 60), odFlush,
				odPut("a", 7, 70), odDelete("c"), odPut("e", 8, 80),
			},
			expected: map[string]int64{"a": 70, "b": 50, "d": 60, "e": 80},
		},
	}

	ctx := context.Background()
	for _, mode := range digestReadModes {
		for _, tt := range tests {
			t.Run(mode.name+"/"+tt.name, func(t *testing.T) {
				b := newDigestTestBucket(t, ctx, mode.pread, 0)
				for _, op := range tt.ops {
					switch op.kind {
					case "put":
						require.NoError(t, b.Put([]byte(op.key), objectDigestTestValue(op.docID, op.update, 8_192)))
					case "delete":
						require.NoError(t, b.Delete([]byte(op.key)))
					case "flush":
						require.NoError(t, b.FlushAndSwitch())
					}
				}

				callbacks := 0
				got := map[string]int64{}
				err := b.ApplyToObjectDigests(ctx, func() { callbacks++ }, func(k []byte, updateTime int64) error {
					_, dup := got[string(k)]
					require.False(t, dup, "key %q visited twice", k)
					got[string(k)] = updateTime
					return nil
				})
				require.NoError(t, err)
				assert.Equal(t, 1, callbacks)
				assert.Equal(t, tt.expected, got)
			})
		}
	}
}
