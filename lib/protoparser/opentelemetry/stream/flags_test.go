package stream

import (
	"reflect"
	"testing"
)

func TestBuildDecodeMetricsOptions_Success(t *testing.T) {
	f := func(promoteScope, promoteAll bool, promoteList, ignoreList []string, resultExpected map[string]struct{}, disableResourceAttrsExpected, disableScopeExpected bool) {
		t.Helper()

		opts, err := buildDecodeMetricsOptions(promoteScope, promoteAll, promoteList, ignoreList)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if opts.DisableScopeMetadata != disableScopeExpected {
			t.Fatalf("unexpected DisableScopeMetadata; got %v; want %v", opts.DisableScopeMetadata, disableScopeExpected)
		}
		if opts.DisableResourceAttributes != disableResourceAttrsExpected {
			t.Fatalf("unexpected DisableResourceAttributes; got %v; want %v", opts.DisableResourceAttributes, disableResourceAttrsExpected)
		}
		if !reflect.DeepEqual(opts.ResourceAttributesList, resultExpected) {
			t.Fatalf("unexpected ResourceAttributesList; got %v; want %v", opts.ResourceAttributesList, resultExpected)
		}
	}

	// defaults: promote scope metadata, promote all resource attributes, no ignore list
	f(true, true, nil, nil, nil, false, false)

	// promoteAllResourceAttributes with an ignore list
	f(true, true, nil, []string{"foo", "bar"}, map[string]struct{}{"foo": {}, "bar": {}}, false, false)

	// promoteResourceAttributes only
	f(true, false, []string{"foo"}, nil, map[string]struct{}{"foo": {}}, true, false)

	// scope metadata promotion disabled
	f(false, true, nil, nil, nil, false, true)
}

func TestBuildDecodeMetricsOptions_Failure(t *testing.T) {
	f := func(promoteScope, promoteAll bool, promoteList, ignoreList []string) {
		t.Helper()

		if _, err := buildDecodeMetricsOptions(promoteScope, promoteAll, promoteList, ignoreList); err == nil {
			t.Fatalf("expected non-nil error for promoteAll=%v, promoteList=%v, ignoreList=%v", promoteAll, promoteList, ignoreList)
		}
	}

	// promoteResourceAttributes set while promoteAllResourceAttributes is enabled
	f(true, true, []string{"foo"}, nil)

	// ignoreResourceAttributes set while promoteAllResourceAttributes is disabled
	f(true, false, []string{"foo"}, []string{"bar"})
	f(true, false, nil, []string{"bar"})
}
