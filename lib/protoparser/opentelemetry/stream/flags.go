package stream

import (
	"flag"
	"fmt"
	"sync"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/flagutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/logger"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/protoparser/opentelemetry/pb"
)

var (
	promoteScopeMetadata = flag.Bool("opentelemetry.promoteScopeMetadata", true, "Whether to promote OpenTelemetry instrumentation scope metadata to metric labels. "+
		"See https://docs.victoriametrics.com/victoriametrics/#sending-data-via-opentelemetry for details")

	promoteAllResourceAttributes = flag.Bool("opentelemetry.promoteAllResourceAttributes", true, "Whether to promote all the OpenTelemetry resource attributes to metric labels. "+
		"See -opentelemetry.ignoreResourceAttributes for excluding only certain resource attributes from the promotion. "+
		"This flag is mutually exclusive with -opentelemetry.promoteResourceAttributes. "+
		"See https://docs.victoriametrics.com/victoriametrics/#sending-data-via-opentelemetry for details")

	promoteResourceAttributes = flagutil.NewArrayString("opentelemetry.promoteResourceAttributes", "Only the listed OpenTelemetry resource attributes are promoted to metric labels. "+
		"This flag is mutually exclusive with -opentelemetry.promoteAllResourceAttributes, so -opentelemetry.promoteAllResourceAttributes=false must be set when using this flag. "+
		"See https://docs.victoriametrics.com/victoriametrics/#sending-data-via-opentelemetry for details")

	ignoreResourceAttributes = flagutil.NewArrayString("opentelemetry.ignoreResourceAttributes", "The listed OpenTelemetry resource attributes aren't promoted to metric labels. "+
		"This flag can be set only when -opentelemetry.promoteAllResourceAttributes is enabled (the default). "+
		"See https://docs.victoriametrics.com/victoriametrics/#sending-data-via-opentelemetry for details")
)

var (
	decodeMetricsOptions     pb.DecodeMetricsOptions
	decodeMetricsOptionsOnce sync.Once
)

// getDecodeMetricsOptions returns pb.DecodeMetricsOptions built from the command-line flags.
//
// The returned value is cached, since the underlying flags cannot change during the program lifetime.
// CheckFlags must be called before this func is invoked for the first time in order to fail fast
// on invalid flag combinations at startup instead of failing on the first ingested request.
func getDecodeMetricsOptions() pb.DecodeMetricsOptions {
	decodeMetricsOptionsOnce.Do(mustInitDecodeMetricsOptions)
	return decodeMetricsOptions
}

// CheckFlags verifies that OpenTelemetry-related command-line flags are set to a valid combination
// and initializes the options used for decoding OpenTelemetry data.
//
// It must be called during app startup right after the command-line flags are parsed,
// so invalid flag combinations are reported immediately instead of on the first ingested request.
func CheckFlags() {
	decodeMetricsOptionsOnce.Do(mustInitDecodeMetricsOptions)
}

func mustInitDecodeMetricsOptions() {
	opts, err := buildDecodeMetricsOptions(*promoteScopeMetadata, *promoteAllResourceAttributes, []string(*promoteResourceAttributes), []string(*ignoreResourceAttributes))
	if err != nil {
		logger.Fatalf("error when parsing -opentelemetry.* command-line flags: %s", err)
	}
	decodeMetricsOptions = opts
}

// buildDecodeMetricsOptions builds pb.DecodeMetricsOptions from the given flag values.
//
// It returns an error if promoteResourceAttributes and ignoreResourceAttributes are used
// with an incompatible value of promoteAllResourceAttributes.
func buildDecodeMetricsOptions(promoteScope, promoteAll bool, promoteList, ignoreList []string) (pb.DecodeMetricsOptions, error) {
	if promoteAll && len(promoteList) > 0 {
		return pb.DecodeMetricsOptions{}, fmt.Errorf("-opentelemetry.promoteResourceAttributes cannot be set when -opentelemetry.promoteAllResourceAttributes is enabled " +
			"(it is enabled by default); set -opentelemetry.promoteAllResourceAttributes=false in order to use -opentelemetry.promoteResourceAttributes")
	}
	if !promoteAll && len(ignoreList) > 0 {
		return pb.DecodeMetricsOptions{}, fmt.Errorf("-opentelemetry.ignoreResourceAttributes can be set only when -opentelemetry.promoteAllResourceAttributes is enabled; " +
			"got -opentelemetry.promoteAllResourceAttributes=false")
	}

	opts := pb.DecodeMetricsOptions{
		DisableScopeMetadata: !promoteScope,
	}
	if promoteAll {
		opts.DisableResourceAttributes = false
		opts.ResourceAttributesList = sliceToSet(ignoreList)
	} else {
		opts.DisableResourceAttributes = true
		opts.ResourceAttributesList = sliceToSet(promoteList)
	}
	return opts, nil
}

func sliceToSet(a []string) map[string]struct{} {
	if len(a) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(a))
	for _, s := range a {
		m[s] = struct{}{}
	}
	return m
}
