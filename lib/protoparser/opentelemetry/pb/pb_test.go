package pb

import (
	"reflect"
	"sort"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompb"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
)

type collectedSample struct {
	suffix string
	labels []prompb.Label
	value  float64
}

type testPusher struct {
	samples []collectedSample
}

func (p *testPusher) PushSample(_ *MetricMetadata, suffix string, ls *promutil.Labels, _ uint64, value float64, _ uint32) {
	labels := make([]prompb.Label, len(ls.Labels))
	for i, l := range ls.Labels {
		labels[i] = prompb.Label{Name: l.Name, Value: l.Value}
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
	p.samples = append(p.samples, collectedSample{
		suffix: suffix,
		labels: labels,
		value:  value,
	})
}

func (p *testPusher) PushMetricMetadata(_ *MetricMetadata) {}

func strPtr(s string) *string { return &s }

func testMetricsData() *MetricsData {
	return &MetricsData{
		ResourceMetrics: []*ResourceMetrics{
			{
				Resource: &Resource{
					Attributes: []*KeyValue{
						{Key: "job", Value: &AnyValue{StringValue: strPtr("vm")}},
						{Key: "instance", Value: &AnyValue{StringValue: strPtr("localhost")}},
					},
				},
				ScopeMetrics: []*ScopeMetrics{
					{
						Scope: &InstrumentationScope{
							Name:    strPtr("foo"),
							Version: strPtr("bar"),
							Attributes: []*KeyValue{
								{Key: "abc", Value: &AnyValue{StringValue: strPtr("qwe")}},
							},
						},
						Metrics: []*Metric{
							{
								Name: "my-gauge",
								Gauge: &Gauge{
									DataPoints: []*NumberDataPoint{
										{
											Attributes: []*KeyValue{
												{Key: "label1", Value: &AnyValue{StringValue: strPtr("value1")}},
											},
											TimeUnixNano: 15_000_000_000,
											DoubleValue:  floatPtr(15),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func floatPtr(v float64) *float64 { return &v }

func TestDecodeMetricsDataPromotion(t *testing.T) {
	f := func(options DecodeMetricsOptions, wantLabels []prompb.Label) {
		t.Helper()

		src := testMetricsData().MarshalProtobuf(nil)
		var p testPusher
		if err := DecodeMetricsData(src, &p, options); err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if len(p.samples) != 1 {
			t.Fatalf("unexpected samples count; got %d; want 1", len(p.samples))
		}
		got := p.samples[0].labels
		want := append([]prompb.Label(nil), wantLabels...)
		sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected labels;\ngot:  %v\nwant: %v", got, want)
		}
	}

	f(DecodeMetricsOptions{}, []prompb.Label{
		{Name: "instance", Value: "localhost"},
		{Name: "job", Value: "vm"},
		{Name: "label1", Value: "value1"},
		{Name: "scope.attributes.abc", Value: "qwe"},
		{Name: "scope.name", Value: "foo"},
		{Name: "scope.version", Value: "bar"},
	})

	f(DecodeMetricsOptions{DisableScopeMetadata: true}, []prompb.Label{
		{Name: "instance", Value: "localhost"},
		{Name: "job", Value: "vm"},
		{Name: "label1", Value: "value1"},
	})

	f(DecodeMetricsOptions{
		DisableResourceAttributes: true,
	}, []prompb.Label{
		{Name: "label1", Value: "value1"},
		{Name: "scope.attributes.abc", Value: "qwe"},
		{Name: "scope.name", Value: "foo"},
		{Name: "scope.version", Value: "bar"},
	})

	f(DecodeMetricsOptions{
		DisableResourceAttributes: true,
		ResourceAttributesList:    map[string]struct{}{"job": {}},
	}, []prompb.Label{
		{Name: "job", Value: "vm"},
		{Name: "label1", Value: "value1"},
		{Name: "scope.attributes.abc", Value: "qwe"},
		{Name: "scope.name", Value: "foo"},
		{Name: "scope.version", Value: "bar"},
	})

	f(DecodeMetricsOptions{
		ResourceAttributesList: map[string]struct{}{"instance": {}},
	}, []prompb.Label{
		{Name: "job", Value: "vm"},
		{Name: "label1", Value: "value1"},
		{Name: "scope.attributes.abc", Value: "qwe"},
		{Name: "scope.name", Value: "foo"},
		{Name: "scope.version", Value: "bar"},
	})

	f(DecodeMetricsOptions{
		DisableScopeMetadata:      true,
		DisableResourceAttributes: true,
		ResourceAttributesList:    map[string]struct{}{"job": {}},
	}, []prompb.Label{
		{Name: "job", Value: "vm"},
		{Name: "label1", Value: "value1"},
	})
}
