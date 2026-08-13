package pb

import (
	"sort"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompb"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
)

type testMetricPusher struct {
	tss []string
}

func (mp *testMetricPusher) PushSample(_ *MetricMetadata, _ string, ls *promutil.Labels, _ uint64, _ float64, _ uint32) {
	labels := append([]prompb.Label{}, ls.Labels...)
	sort.Slice(labels, func(i, j int) bool {
		return labels[i].Name < labels[j].Name
	})
	a := make([]string, len(labels))
	for i, l := range labels {
		a[i] = l.Name + "=" + l.Value
	}
	mp.tss = append(mp.tss, strings.Join(a, ","))
}

func (mp *testMetricPusher) PushMetricMetadata(_ *MetricMetadata) {}

func newTestMetricsData() *MetricsData {
	return &MetricsData{
		ResourceMetrics: []*ResourceMetrics{
			{
				Resource: &Resource{
					Attributes: []*KeyValue{
						{
							Key: "service.name",
							Value: &AnyValue{
								StringValue: new("foo"),
							},
						},
						{
							Key: "service.instance.id",
							Value: &AnyValue{
								StringValue: new("bar"),
							},
						},
					},
				},
				ScopeMetrics: []*ScopeMetrics{
					{
						Scope: &InstrumentationScope{
							Name:    new("myscope"),
							Version: new("v1"),
							Attributes: []*KeyValue{
								{
									Key: "scopekey",
									Value: &AnyValue{
										StringValue: new("scopevalue"),
									},
								},
							},
						},
						Metrics: []*Metric{
							{
								Name: "my_gauge",
								Gauge: &Gauge{
									DataPoints: []*NumberDataPoint{
										{
											Attributes: []*KeyValue{
												{
													Key: "dp_label",
													Value: &AnyValue{
														StringValue: new("dp_value"),
													},
												},
											},
											DoubleValue: new(float64(1)),
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

func TestDecodeMetricsData_DefaultOptions(t *testing.T) {
	data := newTestMetricsData().MarshalProtobuf(nil)

	mp := &testMetricPusher{}
	if err := DecodeMetricsData(data, mp, DecodeMetricsOptions{}); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	if len(mp.tss) != 1 {
		t.Fatalf("unexpected number of pushed samples; got %d; want 1", len(mp.tss))
	}
	got := mp.tss[0]
	want := "dp_label=dp_value,scope.attributes.scopekey=scopevalue,scope.name=myscope,scope.version=v1,service.instance.id=bar,service.name=foo"
	if got != want {
		t.Fatalf("unexpected labels;\ngot:  %s\nwant: %s", got, want)
	}
}

func TestDecodeMetricsData_DisableScopeMetadata(t *testing.T) {
	data := newTestMetricsData().MarshalProtobuf(nil)

	mp := &testMetricPusher{}
	opts := DecodeMetricsOptions{
		DisableScopeMetadata: true,
	}
	if err := DecodeMetricsData(data, mp, opts); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	got := mp.tss[0]
	want := "dp_label=dp_value,service.instance.id=bar,service.name=foo"
	if got != want {
		t.Fatalf("unexpected labels;\ngot:  %s\nwant: %s", got, want)
	}
}

func TestDecodeMetricsData_IgnoreResourceAttributes(t *testing.T) {
	data := newTestMetricsData().MarshalProtobuf(nil)

	mp := &testMetricPusher{}
	opts := DecodeMetricsOptions{
		DisableResourceAttributes: false,
		ResourceAttributesList: map[string]struct{}{
			"service.instance.id": {},
		},
	}
	if err := DecodeMetricsData(data, mp, opts); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	got := mp.tss[0]
	want := "dp_label=dp_value,scope.attributes.scopekey=scopevalue,scope.name=myscope,scope.version=v1,service.name=foo"
	if got != want {
		t.Fatalf("unexpected labels;\ngot:  %s\nwant: %s", got, want)
	}
}

func TestDecodeMetricsData_PromoteOnlyListedResourceAttributes(t *testing.T) {
	data := newTestMetricsData().MarshalProtobuf(nil)

	mp := &testMetricPusher{}
	opts := DecodeMetricsOptions{
		DisableResourceAttributes: true,
		ResourceAttributesList: map[string]struct{}{
			"service.name": {},
		},
	}
	if err := DecodeMetricsData(data, mp, opts); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	got := mp.tss[0]
	want := "dp_label=dp_value,scope.attributes.scopekey=scopevalue,scope.name=myscope,scope.version=v1,service.name=foo"
	if got != want {
		t.Fatalf("unexpected labels;\ngot:  %s\nwant: %s", got, want)
	}
}

func TestDecodeMetricsData_DisableResourceAttributesNoneListed(t *testing.T) {
	data := newTestMetricsData().MarshalProtobuf(nil)

	mp := &testMetricPusher{}
	opts := DecodeMetricsOptions{
		DisableResourceAttributes: true,
	}
	if err := DecodeMetricsData(data, mp, opts); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	got := mp.tss[0]
	want := "dp_label=dp_value,scope.attributes.scopekey=scopevalue,scope.name=myscope,scope.version=v1"
	if got != want {
		t.Fatalf("unexpected labels;\ngot:  %s\nwant: %s", got, want)
	}
}
