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
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCursorDigest_MatchesTruncatedFullCursor(t *testing.T) {
	ctx := context.Background()
	const prefix = 8

	modes := []struct {
		name string
		opts []BucketOption
	}{
		{"mmap", nil},
		{"pread", []BucketOption{WithPread(true), WithMinMMapSize(0)}},
	}

	// sizes vary so the reusable value buffer grows and shrinks across nodes
	val := func(round, i int) []byte {
		size := []int{0, 3, prefix, prefix + 1, 5000, 1, 70000}[i%7]
		return bytes.Repeat([]byte{byte('a' + round), byte(i)}, size)[:size]
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			b := newReusableTestBucket(t, ctx, mode.opts...)
			defer b.Shutdown(ctx)

			for i := 0; i < 60; i++ {
				require.NoError(t, b.Put([]byte(fmt.Sprintf("key-%03d", i)), val(0, i)))
			}
			require.NoError(t, b.FlushAndSwitch())
			for i := 30; i < 90; i++ {
				require.NoError(t, b.Put([]byte(fmt.Sprintf("key-%03d", i)), val(1, i)))
			}
			for i := 0; i < 60; i += 7 {
				require.NoError(t, b.Delete([]byte(fmt.Sprintf("key-%03d", i))))
			}
			require.NoError(t, b.FlushAndSwitch())
			for i := 80; i < 100; i++ {
				require.NoError(t, b.Put([]byte(fmt.Sprintf("key-%03d", i)), val(2, i)))
			}

			truncate := func(in []string) []string {
				out := make([]string, len(in))
				for i, s := range in {
					k, v, _ := bytes.Cut([]byte(s), []byte("="))
					if len(v) > prefix {
						v = v[:prefix]
					}
					out[i] = string(k) + "=" + string(v)
				}
				return out
			}

			want := truncate(drainCursor(b.CursorOnDisk()))
			require.NotEmpty(t, want)
			require.Equal(t, want, drainCursor(b.CursorOnDiskDigest(prefix)))
			require.Equal(t, drainCursor(b.CursorOnDisk()), drainCursor(b.CursorOnDiskDigest(0)))

			// memtable-only keys (>= 90) stay full; disk keys are truncated
			full := drainCursor(b.Cursor())
			got := drainCursor(b.CursorReplaceDigestReusable(prefix))
			require.Equal(t, len(full), len(got))
			for i := range full {
				k, _, _ := bytes.Cut([]byte(full[i]), []byte("="))
				if string(k) >= "key-080" {
					require.Equal(t, full[i], got[i])
				} else {
					require.Equal(t, truncate(full[i:i+1])[0], got[i])
				}
			}

			for _, target := range []string{"", "key-000", "key-031", "key-059", "key-085", "key-999"} {
				require.Equal(t,
					truncate(drainSeek(b.CursorOnDisk(), []byte(target))),
					drainSeek(b.CursorOnDiskDigest(prefix), []byte(target)),
					"Seek(%q)", target)
			}
		})
	}
}
